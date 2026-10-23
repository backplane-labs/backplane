package bindings

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.yaml.in/yaml/v3"

	"github.com/gopherex/xlog"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
)

// FilesAuthor is the author of the versions the wiring files write.
const FilesAuthor = "wiring"

// ErrWiringFile: a wiring file is not a valid definition of bindings and
// rules.
var ErrWiringFile = errors.New("bindings: wiring file")

// defaultFilesPoll is how often the directory is read again.
const defaultFilesPoll = 5 * time.Second

// ruleNamespace derives a wiring file's rule id from the rule's name, so
// every replica and every restart write the same rule.
//
//nolint:gochecknoglobals // a constant uuid
var ruleNamespace = uuid.MustParse("58482934-c401-4ec7-8779-ba857c2e954e")

// FileRuleID is the id of the rule a wiring file names name.
func FileRuleID(name string) uuid.UUID { return uuid.NewSHA1(ruleNamespace, []byte(name)) }

// Files keeps the bindings and rules of a wiring directory in force: the
// directory is the desired state, and the console only reads what it holds.
//
// Every *.yaml / *.yml file under the directory (dot-names skipped, so a
// Kubernetes ConfigMap mount works as is) holds one or more documents:
//
//	bindings:
//	  - hook: kratos.Deliver        # BindingDefinition, as the console edits it
//	    steps: {...}
//	rules:
//	  - name: welcome               # the rule's identity
//	    event: kratos.IdentityCreated
//	    steps: {...}
//
// A pass saves what differs from the current version as a new version
// managed by its file, and tombstones what a file held before and holds no
// more. A definition that does not compile against the manifests waits —
// for the services to register or for a fixed file — with the version in
// force untouched; a directory that does not read whole changes nothing.
// Replicas run it side by side: saves are conditional on the version read,
// so they converge.
type Files struct {
	deps.Component

	m    *Manager
	dir  string
	poll time.Duration

	mu       sync.Mutex
	reported map[string]string // entry -> the problem last logged
	settled  string            // the fingerprint of the last pass that applied everything
}

// NewFiles creates the component under parent: the directory dir, read
// every poll (0: 5s) and on every new manifest.
func NewFiles(parent deps.Scope, m *Manager, dir string, poll time.Duration) *Files {
	if poll <= 0 {
		poll = defaultFilesPoll
	}

	f := &Files{
		Component: deps.NewComponent(parent, "wiring-files"),
		m:         m, dir: dir, poll: poll, reported: map[string]string{},
	}
	f.Go(f.loop)

	return f
}

func (f *Files) loop(ctx context.Context) error {
	manifests := f.m.src.Changes(ctx)

	tick := time.NewTicker(f.poll)
	defer tick.Stop()

	for {
		f.pass(ctx)

		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		case <-manifests:
		}
	}
}

// fileBinding and fileRule are entries of the directory.
type fileBinding struct {
	file    string
	binding Binding
}

type fileRule struct {
	file string
	name string
	rule Rule
}

// desired is what the directory holds.
type desired struct {
	bindings map[string]fileBinding // by hook
	rules    map[uuid.UUID]fileRule
	digest   string
}

// pass reconciles once.
func (f *Files) pass(ctx context.Context) {
	want, err := readWiring(f.dir)
	if err != nil {
		f.report("directory", "warn", fmt.Sprintf("wiring directory not applied: %v", err))

		return
	}

	f.report("directory", "", "")

	cat := f.m.src.Current()
	if cat.Index == 0 {
		return
	}

	stamp, err := f.m.store.Get().Q.BindingsStamp(ctx)
	if err != nil {
		if ctx.Err() == nil {
			f.Log().Debug("wiring files: bindings stamp", xlog.Err(err))
		}

		return
	}

	fingerprint := fmt.Sprint(want.digest, "/", cat.Index, "/", stamp.Bindings, "/", stamp.Rules)

	f.mu.Lock()
	settled := f.settled == fingerprint
	f.mu.Unlock()

	if settled {
		return
	}

	clean, err := f.apply(ctx, want)
	if err != nil {
		if ctx.Err() == nil {
			f.Log().Warn("wiring files: pass failed", xlog.Err(err))
		}

		return
	}

	if clean {
		// The pass wrote versions, so the stamp moved: the next pass reads
		// it again and settles on what it wrote.
		f.mu.Lock()
		f.settled = fingerprint
		f.mu.Unlock()
	}
}

// apply saves the differences; clean is false when an entry waits or lost
// a race (the next pass tries again).
func (f *Files) apply(ctx context.Context, want desired) (bool, error) {
	current, err := f.m.CurrentAll(ctx)
	if err != nil {
		return false, err
	}

	rules, err := f.m.Rules(ctx)
	if err != nil {
		return false, err
	}

	bindingsClean := f.applyBindings(ctx, want.bindings, current)
	rulesClean := f.applyRules(ctx, want.rules, rules)

	return bindingsClean && rulesClean, nil
}

func (f *Files) applyBindings(ctx context.Context, want map[string]fileBinding, current []BindingVersion) bool {
	clean := true

	byHook := make(map[string]*BindingVersion, len(current))
	for i := range current {
		byHook[current[i].Hook] = &current[i]
	}

	for _, hook := range inOrder(want) {
		e := want[hook]
		cur := byHook[hook]

		if cur != nil && !cur.Deleted && cur.ManagedBy == e.file && cur.Binding.Equal(e.binding) {
			f.report("binding "+hook, "", "")

			continue
		}

		var base int64
		if cur != nil {
			base = cur.Version
		}

		saved, err := f.m.save(ctx, e.binding, "from "+e.file, 0, At(base), writer{files: true, file: e.file})
		clean = f.outcome("binding "+hook, e.file, "applied", saved.Violations, err) && clean
	}

	for _, hook := range inOrder(byHook) {
		cur := byHook[hook]
		if _, held := want[hook]; held || cur.ManagedBy == "" || cur.Deleted {
			continue
		}

		_, err := f.m.insertBinding(ctx, hook, nil, FilesAuthor, "removed from "+cur.ManagedBy, 0, At(cur.Version),
			writer{files: true})
		clean = f.outcome("binding "+hook, cur.ManagedBy, "removed", nil, err) && clean
	}

	return clean
}

func (f *Files) applyRules(ctx context.Context, want map[uuid.UUID]fileRule, rules []RuleEntry) bool {
	clean := true

	byID := make(map[uuid.UUID]*RuleVersion, len(rules))
	for i := range rules {
		byID[rules[i].ID] = &rules[i].Current
	}

	for _, id := range inOrder(want) {
		e := want[id]
		cur := byID[id]

		if cur != nil && !cur.Deleted && cur.ManagedBy == e.file && cur.Name == e.name && cur.Rule.Equal(e.rule) {
			f.report("rule "+e.name, "", "")

			continue
		}

		var base int64
		if cur != nil {
			base = cur.Version
		}

		saved, err := f.m.saveRule(ctx, id, e.name, e.rule, "from "+e.file, 0, At(base), writer{files: true, file: e.file})
		clean = f.outcome("rule "+e.name, e.file, "applied", saved.Violations, err) && clean
	}

	for _, id := range inOrder(byID) {
		cur := byID[id]
		if _, held := want[id]; held || cur.ManagedBy == "" || cur.Deleted {
			continue
		}

		_, err := f.m.insertRule(ctx, id, cur.Name, nil, FilesAuthor, "removed from "+cur.ManagedBy, 0,
			At(cur.Version), writer{files: true})
		clean = f.outcome("rule "+cur.Name, cur.ManagedBy, "removed", nil, err) && clean
	}

	return clean
}

// outcome logs a write's result (what it did: applied, removed), problems
// once each; ok when it was written.
func (f *Files) outcome(entry, file, did string, violations []Violation, err error) bool {
	switch {
	case errors.Is(err, ErrConflict):
		return false
	case err != nil:
		f.report(entry, "warn", fmt.Sprintf("%s from %s not applied: %v", entry, file, err))

		return false
	case len(violations) > 0:
		level, why := "warn", "does not compile"
		if waiting(violations) {
			level, why = "info", "waits for the services it names"
		}

		f.report(entry, level, fmt.Sprintf("%s from %s %s: %s", entry, file, why, violations[0]))

		return false
	}

	f.report(entry, "", "")
	f.Log().Info("wiring file "+did, xlog.String("entry", entry), xlog.String("file", file))

	return true
}

// waiting reports whether every violation names something no manifest
// declares yet: the service is not registered (or not yet upgraded).
func waiting(vs []Violation) bool {
	for _, v := range vs {
		if v.Code != CodeUnknownHook && v.Code != CodeUnknownEvent && v.Code != CodeUnknownActivity {
			return false
		}
	}

	return true
}

// report logs msg at level when it differs from what was logged for entry
// last; level "" clears the entry.
func (f *Files) report(entry, level, msg string) {
	f.mu.Lock()
	last := f.reported[entry]

	if level == "" {
		delete(f.reported, entry)
	} else {
		f.reported[entry] = msg
	}
	f.mu.Unlock()

	switch {
	case level == "" || last == msg:
	case level == "info":
		f.Log().Info(msg)
	default:
		f.Log().Warn(msg)
	}
}

// readWiring reads the directory whole: any error fails it all.
func readWiring(dir string) (desired, error) {
	want := desired{bindings: map[string]fileBinding{}, rules: map[uuid.UUID]fileRule{}}
	sum := sha256.New()

	var files []string

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if path != dir && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}

			return nil
		}

		if ext := filepath.Ext(path); !d.IsDir() && (ext == ".yaml" || ext == ".yml") {
			files = append(files, path)
		}

		return nil
	})
	if err != nil {
		return desired{}, fmt.Errorf("read %s: %w", dir, err)
	}

	slices.Sort(files)

	for _, path := range files {
		rel, _ := filepath.Rel(dir, path)

		data, err := os.ReadFile(path)
		if err != nil {
			return desired{}, fmt.Errorf("read %s: %w", rel, err)
		}

		_, _ = fmt.Fprintf(sum, "%s\x00%d\x00", rel, len(data))
		_, _ = sum.Write(data)

		if err := want.add(rel, data); err != nil {
			return desired{}, fmt.Errorf("%s: %w", rel, err)
		}
	}

	want.digest = hex.EncodeToString(sum.Sum(nil))

	return want, nil
}

// wiringDocument is one YAML document of a wiring file.
type wiringDocument struct {
	Bindings []map[string]any `yaml:"bindings"`
	Rules    []map[string]any `yaml:"rules"`
	// Schedules are ops.ScheduleFiles's.
	Schedules []map[string]any `yaml:"schedules"`
}

// add reads one file's documents into want.
func (want desired) add(file string, data []byte) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)

	for {
		var doc wiringDocument

		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return nil
		}

		if err != nil {
			return fmt.Errorf("yaml: %w", err)
		}

		for i, tree := range doc.Bindings {
			if err := want.addBinding(file, tree); err != nil {
				return fmt.Errorf("bindings[%d]: %w", i, err)
			}
		}

		for i, tree := range doc.Rules {
			if err := want.addRule(file, tree); err != nil {
				return fmt.Errorf("rules[%d]: %w", i, err)
			}
		}
	}
}

func (want desired) addBinding(file string, tree map[string]any) error {
	var m consolev1.BindingDefinition
	if err := decodeTree(tree, &m); err != nil {
		return err
	}

	hook := m.GetHook()
	if hook == "" {
		return fmt.Errorf("%w: hook is required", ErrWiringFile)
	}

	if prev, dup := want.bindings[hook]; dup {
		return fmt.Errorf("%w: the binding of %s is defined in %s too", ErrWiringFile, hook, prev.file)
	}

	want.bindings[hook] = fileBinding{file: file, binding: BindingFromPB(&m)}

	return nil
}

func (want desired) addRule(file string, tree map[string]any) error {
	name, _ := tree["name"].(string)
	if name = strings.TrimSpace(name); name == "" {
		return fmt.Errorf("%w: name is required: it is the rule's identity", ErrWiringFile)
	}

	delete(tree, "name")

	var m consolev1.RuleDefinition
	if err := decodeTree(tree, &m); err != nil {
		return fmt.Errorf("rule %s: %w", name, err)
	}

	id := FileRuleID(name)
	if prev, dup := want.rules[id]; dup {
		return fmt.Errorf("%w: rule %s is defined in %s too", ErrWiringFile, name, prev.file)
	}

	want.rules[id] = fileRule{file: file, name: name, rule: RuleFromPB(&m)}

	return nil
}

// inOrder are m's keys in order: passes write in a stable order.
func inOrder[K comparable, V any](m map[K]V) []K {
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	slices.SortFunc(keys, func(a, b K) int { return strings.Compare(fmt.Sprint(a), fmt.Sprint(b)) })

	return keys
}
