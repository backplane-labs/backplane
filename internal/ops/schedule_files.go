package ops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	schedulepb "go.temporal.io/api/schedule/v1"
	"go.temporal.io/api/serviceerror"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.yaml.in/yaml/v3"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/gopherex/xlog"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/wire"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
)

// ErrScheduleFile: a wiring file's schedule is not a valid definition.
var ErrScheduleFile = errors.New("ops: wiring file schedule")

// ErrManaged: the schedule is a wiring file's; edit the file instead.
var ErrManaged = fmt.Errorf("%w: schedule is managed by wiring file", ErrPrecondition)

// defaultScheduleFilesPoll is how often the directory is read again.
const defaultScheduleFilesPoll = 5 * time.Second

// scheduleListPage bounds one page of the managed schedules' listing.
const scheduleListPage = 100

// ScheduleFiles keeps the schedules of a wiring directory in force in
// Temporal: the directory is the desired state. The same *.yaml / *.yml
// files as the bindings and rules (bindings.Files) hold them:
//
//	schedules:
//	  - service: media
//	    name: collect
//	    workflow: Collect
//	    input: {older_than: 24h}
//	    timing: {cron: ["0 3 * * *"], timezone: Europe/Moscow}  # or interval: 1h
//	    execution_timeout: 1h
//	    paused: false        # only when the schedule is created
//
// A pass creates what is missing, updates what differs from its file (the
// schedule's state — paused, its runs — is the operator's and stays), takes
// over a schedule of the same id created by hand (deleted and created
// again), and deletes the schedules a file held before and holds no more.
// A schedule whose workflow no manifest declares yet waits. Replicas run it
// side by side; updates are conditional on the schedule read.
type ScheduleFiles struct {
	deps.Component

	a    ScheduleAPI
	dir  string
	poll time.Duration

	mu       sync.Mutex
	reported map[string]string
	settled  string
}

// NewScheduleFiles creates the component under parent: the directory dir,
// read every poll (0: 5s) and on every new manifest.
func NewScheduleFiles(parent deps.Scope, o *Ops, dir string, poll time.Duration) *ScheduleFiles {
	if poll <= 0 {
		poll = defaultScheduleFilesPoll
	}

	f := &ScheduleFiles{
		Component: deps.NewComponent(parent, "wiring-schedules"),
		a:         o.Schedules(), dir: dir, poll: poll, reported: map[string]string{},
	}
	f.Go(f.loop)

	return f
}

func (f *ScheduleFiles) loop(ctx context.Context) error {
	manifests := f.a.o.src.Changes(ctx)

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

// fileSchedule is one schedule of the directory.
type fileSchedule struct {
	file    string
	service string
	name    string
	paused  bool
	def     *consolev1.ScheduleDefinition
	// digest of what is applied: file, workflow, input, timing, timeout.
	digest string
}

func (e fileSchedule) id() string { return wire.ScheduleID(e.service, e.name) }

// pass reconciles once.
func (f *ScheduleFiles) pass(ctx context.Context) {
	want, digest, err := readSchedules(f.dir)
	if err != nil {
		f.report("directory", "warn", fmt.Sprintf("wiring schedules not applied: %v", err))

		return
	}

	f.report("directory", "", "")

	cat := f.a.o.src.Current()
	if cat.Index == 0 {
		return
	}

	c, err := f.a.o.client()
	if err != nil {
		if len(want) > 0 {
			f.report("temporal", "warn", fmt.Sprintf("wiring schedules wait for Temporal: %v", err))
		}

		return
	}

	f.report("temporal", "", "")

	fingerprint := fmt.Sprint(digest, "/", cat.Index)

	f.mu.Lock()
	settled := f.settled == fingerprint
	f.mu.Unlock()

	if settled {
		return
	}

	clean := true

	for _, id := range inOrderKeys(want) {
		clean = f.apply(ctx, c, want[id]) && clean
	}

	if err := f.removeStale(ctx, c, want); err != nil {
		if ctx.Err() == nil {
			f.Log().Warn("wiring schedules: removal failed", xlog.Err(err))
		}

		clean = false
	}

	if clean {
		f.mu.Lock()
		f.settled = fingerprint
		f.mu.Unlock()
	}
}

// apply makes one schedule what its file says; ok when it is.
func (f *ScheduleFiles) apply(ctx context.Context, c client.Client, e fileSchedule) bool {
	entry := "schedule " + e.id()
	api := c.WorkflowService()

	action, err := f.a.scheduleAction(ctx, e.service, e.id(), e.def)
	if errors.Is(err, ErrNotDeclared) || errors.Is(err, ErrNoService) {
		f.report(entry, "info", fmt.Sprintf("%s from %s waits for the service to declare %s", entry, e.file,
			e.def.GetWorkflow()))

		return false
	}

	if err == nil {
		err = markAction(action.GetStartWorkflow(), e)
	}

	spec, specErr := scheduleTiming(e.def.GetTiming())
	if err = errors.Join(err, specErr); err != nil {
		f.report(entry, "warn", fmt.Sprintf("%s from %s not applied: %v", entry, e.file, err))

		return false
	}

	desc, err := api.DescribeSchedule(ctx, &workflowservice.DescribeScheduleRequest{
		Namespace: f.a.o.namespace, ScheduleId: e.id(),
	})

	switch {
	case isNotFound(err):
		return f.done(entry, e.file, "created", f.create(ctx, api, e, spec, action))
	case err != nil:
		return f.done(entry, e.file, "applied", err)
	case managedBy(desc.GetMemo()) == "":
		// Created by hand: the file takes it over.
		if _, err := api.DeleteSchedule(ctx, &workflowservice.DeleteScheduleRequest{
			Namespace: f.a.o.namespace, ScheduleId: e.id(), Identity: bindingsAuthor,
		}); err != nil && !isNotFound(err) {
			return f.done(entry, e.file, "taken over", err)
		}

		return f.done(entry, e.file, "taken over", f.create(ctx, api, e, spec, action))
	case fingerprintOf(desc.GetSchedule().GetAction().GetStartWorkflow().GetMemo()) == e.digest:
		f.report(entry, "", "")

		return true
	}

	schedule := proto.CloneOf(desc.GetSchedule())
	schedule.Spec, schedule.Action = spec, action

	_, err = api.UpdateSchedule(ctx, &workflowservice.UpdateScheduleRequest{
		Namespace: f.a.o.namespace, ScheduleId: e.id(), Schedule: schedule,
		ConflictToken: desc.GetConflictToken(), Identity: bindingsAuthor, RequestId: uuid.NewString(),
	})

	return f.done(entry, e.file, "updated", err)
}

func (f *ScheduleFiles) create(
	ctx context.Context, api workflowservice.WorkflowServiceClient, e fileSchedule,
	spec *schedulepb.ScheduleSpec, action *schedulepb.ScheduleAction,
) error {
	owner, _ := converter.GetDefaultDataConverter().ToPayload(e.service)
	file, _ := converter.GetDefaultDataConverter().ToPayload(e.file)

	_, err := api.CreateSchedule(ctx, &workflowservice.CreateScheduleRequest{
		Namespace: f.a.o.namespace, ScheduleId: e.id(), Identity: bindingsAuthor, RequestId: uuid.NewString(),
		Memo: &commonpb.Memo{Fields: map[string]*commonpb.Payload{wire.MemoService: owner, wire.MemoWiring: file}},
		Schedule: &schedulepb.Schedule{
			Spec: spec, Action: action, State: &schedulepb.ScheduleState{Paused: e.paused},
			Policies: &schedulepb.SchedulePolicies{OverlapPolicy: enumspb.SCHEDULE_OVERLAP_POLICY_SKIP},
		},
	})
	if alreadyExists(err) {
		return nil // another replica created it: the next pass compares
	}

	return err //nolint:wrapcheck // reported as is
}

// removeStale deletes the managed schedules no file holds any more.
func (f *ScheduleFiles) removeStale(ctx context.Context, c client.Client, want map[string]fileSchedule) error {
	var token []byte

	for {
		page, err := c.WorkflowService().ListSchedules(ctx, &workflowservice.ListSchedulesRequest{
			Namespace: f.a.o.namespace, MaximumPageSize: scheduleListPage, NextPageToken: token,
		})
		if err != nil {
			return fmt.Errorf("list schedules: %w", err)
		}

		for _, entry := range page.GetSchedules() {
			id := entry.GetScheduleId()

			file := managedBy(entry.GetMemo())
			if _, held := want[id]; held || file == "" {
				continue
			}

			_, err := c.WorkflowService().DeleteSchedule(ctx, &workflowservice.DeleteScheduleRequest{
				Namespace: f.a.o.namespace, ScheduleId: id, Identity: bindingsAuthor,
			})
			if err != nil && !isNotFound(err) {
				return fmt.Errorf("delete %s: %w", id, err)
			}

			f.report("schedule "+id, "", "")
			f.Log().Info("wiring file removed", xlog.String("entry", "schedule "+id), xlog.String("file", file))
		}

		if token = page.GetNextPageToken(); len(token) == 0 {
			return nil
		}
	}
}

// done logs a write's outcome; ok when it was written.
func (f *ScheduleFiles) done(entry, file, did string, err error) bool {
	if err != nil {
		f.report(entry, "warn", fmt.Sprintf("%s from %s not applied: %v", entry, file, err))

		return false
	}

	f.report(entry, "", "")
	f.Log().Info("wiring file "+did, xlog.String("entry", entry), xlog.String("file", file))

	return true
}

// report logs msg at level when it differs from what was logged for entry
// last; level "" clears the entry.
func (f *ScheduleFiles) report(entry, level, msg string) {
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

func isNotFound(err error) bool {
	var missing *serviceerror.NotFound

	return errors.As(err, &missing) || status.Code(err) == codes.NotFound
}

func alreadyExists(err error) bool {
	var started *serviceerror.WorkflowExecutionAlreadyStarted

	var exists *serviceerror.AlreadyExists

	return errors.As(err, &started) || errors.As(err, &exists) || status.Code(err) == codes.AlreadyExists
}

// bindingsAuthor is the identity the wiring files write with.
const bindingsAuthor = "wiring"

// markAction stamps the file and the definition's digest on the action.
func markAction(start *workflowpb.NewWorkflowExecutionInfo, e fileSchedule) error {
	if start == nil {
		return fmt.Errorf("%w: no workflow action", ErrScheduleFile)
	}

	file, _ := converter.GetDefaultDataConverter().ToPayload(e.file)
	digest, _ := converter.GetDefaultDataConverter().ToPayload(e.digest)

	start.Memo = &commonpb.Memo{Fields: map[string]*commonpb.Payload{wire.MemoWiring: file, wire.MemoFingerprint: digest}}

	return nil
}

// managedBy is the wiring file a memo names; "" when none.
func managedBy(memo *commonpb.Memo) string {
	return memoString(memo, wire.MemoWiring)
}

func fingerprintOf(memo *commonpb.Memo) string { return memoString(memo, wire.MemoFingerprint) }

func memoString(memo *commonpb.Memo, key string) string {
	payload := memo.GetFields()[key]
	if payload == nil {
		return ""
	}

	var out string
	if converter.GetDefaultDataConverter().FromPayload(payload, &out) != nil {
		return ""
	}

	return out
}

// readSchedules reads the directory's schedules whole: any error fails
// it all. The digest covers every file, so a change anywhere starts a
// pass.
func readSchedules(dir string) (map[string]fileSchedule, string, error) {
	want := map[string]fileSchedule{}
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
		return nil, "", fmt.Errorf("read %s: %w", dir, err)
	}

	slices.Sort(files)

	for _, path := range files {
		rel, _ := filepath.Rel(dir, path)

		data, err := os.ReadFile(path)
		if err != nil {
			return nil, "", fmt.Errorf("read %s: %w", rel, err)
		}

		_, _ = fmt.Fprintf(sum, "%s\x00%d\x00", rel, len(data))
		_, _ = sum.Write(data)

		if err := addSchedules(want, rel, data); err != nil {
			return nil, "", fmt.Errorf("%s: %w", rel, err)
		}
	}

	return want, hex.EncodeToString(sum.Sum(nil)), nil
}

// addSchedules reads one file's schedules into want; the rest of the
// documents is the bindings' (bindings.Files checks it).
func addSchedules(want map[string]fileSchedule, file string, data []byte) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))

	for {
		var doc struct {
			Schedules []map[string]any `yaml:"schedules"`
		}

		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return nil
		}

		if err != nil {
			return fmt.Errorf("yaml: %w", err)
		}

		for i, tree := range doc.Schedules {
			e, err := parseSchedule(file, tree)
			if err != nil {
				return fmt.Errorf("schedules[%d]: %w", i, err)
			}

			if prev, dup := want[e.id()]; dup {
				return fmt.Errorf("%w: schedule %s is defined in %s too", ErrScheduleFile, e.id(), prev.file)
			}

			want[e.id()] = e
		}
	}
}

// parseSchedule reads one entry: service, name and paused are the file's,
// the rest a ScheduleDefinition with input as YAML.
func parseSchedule(file string, tree map[string]any) (fileSchedule, error) {
	e := fileSchedule{file: file}

	e.service, _ = tree["service"].(string)
	e.name, _ = tree["name"].(string)
	e.paused, _ = tree["paused"].(bool)

	if _, err := scheduleID(e.service, e.name); err != nil {
		return e, fmt.Errorf("%w: service and name are required", ErrScheduleFile)
	}

	input, hasInput := tree["input"]

	for _, k := range []string{"service", "name", "paused", "input"} {
		delete(tree, k)
	}

	e.def = &consolev1.ScheduleDefinition{}
	goDurations(tree, e.def.ProtoReflect().Descriptor())

	raw, err := json.Marshal(tree)
	if err != nil {
		return e, fmt.Errorf("%w: %s: %w", ErrScheduleFile, e.id(), err)
	}

	if err := protojson.Unmarshal(raw, e.def); err != nil {
		return e, fmt.Errorf("%w: %s: %w", ErrScheduleFile, e.id(), err)
	}

	if hasInput {
		in, err := json.Marshal(input)
		if err != nil {
			return e, fmt.Errorf("%w: %s: input: %w", ErrScheduleFile, e.id(), err)
		}

		e.def.Input = string(in)
	}

	canonical, _ := protojson.MarshalOptions{UseProtoNames: true}.Marshal(e.def)
	sum := sha256.Sum256(fmt.Appendf(nil, "%s\x00%s\x00", file, canonical))
	e.digest = hex.EncodeToString(sum[:])

	return e, nil
}

// goDurations rewrites the google.protobuf.Duration fields of tree, a
// message md as YAML, from Go durations ("30m", "1h30m") to the seconds
// protojson reads ("1800s"), as everywhere else in backplane's YAML. What is
// not a Go duration is left for protojson to judge.
func goDurations(tree map[string]any, md protoreflect.MessageDescriptor) {
	for key, value := range tree {
		fd := md.Fields().ByJSONName(key)
		if fd == nil {
			fd = md.Fields().ByName(protoreflect.Name(key))
		}

		if fd == nil || fd.Message() == nil || fd.IsMap() {
			continue
		}

		convert := func(v any) any {
			if fd.Message().FullName() == "google.protobuf.Duration" {
				if text, ok := v.(string); ok {
					if d, err := time.ParseDuration(text); err == nil {
						return protoSeconds(d)
					}
				}

				return v
			}

			if nested, ok := v.(map[string]any); ok {
				goDurations(nested, fd.Message())
			}

			return v
		}

		if list, ok := value.([]any); ok && fd.IsList() {
			for i := range list {
				list[i] = convert(list[i])
			}

			continue
		}

		tree[key] = convert(value)
	}
}

// protoSeconds is d as protojson writes a Duration.
func protoSeconds(d time.Duration) string {
	if d%time.Second == 0 {
		return fmt.Sprintf("%ds", d/time.Second)
	}

	return strconv.FormatFloat(d.Seconds(), 'f', -1, 64) + "s"
}

// inOrderKeys are m's keys sorted.
func inOrderKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	slices.Sort(keys)

	return keys
}
