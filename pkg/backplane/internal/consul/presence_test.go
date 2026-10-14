package consul_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"
	"google.golang.org/protobuf/proto"

	"github.com/gopherex/xlog"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/configrt"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/consul"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/testlog"
)

type group struct{ ctx context.Context }

func (g group) Go(fn func(context.Context) error) { go func() { _ = fn(g.ctx) }() }

// dropped is a group whose node stopped before running anything.
type dropped struct{}

func (dropped) Go(func(context.Context) error) {}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}

		time.Sleep(5 * time.Millisecond)
	}

	t.Fatalf("not reached: %s", what)
}

// start runs a presence with fast clocks; it is stopped on cleanup.
func start(t *testing.T, params consul.Params) *consul.Presence {
	t.Helper()

	if params.Log == nil {
		params.Log = testlog.Discard()
	}

	if params.Manifest == nil {
		params.Manifest = &backplanev1.Manifest{Service: params.Identity.Service, Version: params.Identity.Version}
	}

	p, err := consul.New(params)
	if err != nil {
		t.Fatal(err)
	}

	p.UseFastTiming()

	ctx, cancel := context.WithCancel(context.Background())

	if params.Register {
		// Serving from the first establish: registration is part of it.
		if err := p.Register(ctx); err != nil {
			t.Fatal(err)
		}
	}

	t.Cleanup(func() {
		cancel()

		if err := p.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	if err := p.Start(ctx, group{ctx}); err != nil {
		t.Fatal(err)
	}

	return p
}

// logBuf collects log output across goroutines.
type logBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logBuf) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.buf.Write(b)
}

// has reports whether some log line contains all parts.
func (l *logBuf) has(parts ...string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	for line := range strings.Lines(l.buf.String()) {
		if !slices.ContainsFunc(parts, func(s string) bool { return !strings.Contains(line, s) }) {
			return true
		}
	}

	return false
}

type fakeConfig struct {
	mu     sync.Mutex
	values []byte
	eff    configrt.Effective // everything but Values
	fn     func()
}

func (c *fakeConfig) Effective() configrt.Effective {
	c.mu.Lock()
	defer c.mu.Unlock()

	eff := c.eff
	eff.Values = c.values

	return eff
}

func (c *fakeConfig) OnChange(fn func()) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.fn = fn
}

func (c *fakeConfig) set(values string) {
	c.mu.Lock()
	c.values = []byte(values)
	fn := c.fn
	c.mu.Unlock()

	fn()
}

// fakeConsul serves the part of the Consul HTTP API presence uses, with
// fault injection. Sessions never expire on their own.
type fakeConsul struct {
	mu            sync.Mutex
	seq           int
	kv            map[string]fakeKV
	sessions      map[string]string // id → name
	events        []string          // "create <id>" / "destroy <id>", in order
	renews        int
	failRenew     map[string]bool // renews of these sessions answer 500
	failRenews    int             // the next n renews answer 500
	failAcquires  int
	failRegisters int
	registrations []api.AgentServiceRegistration
	deregisters   int
	manifestOnCAS []byte // a competing process wins the next manifest creation
}

type fakeKV struct {
	value   []byte
	session string
}

func newFake(t *testing.T) (*fakeConsul, *api.Client) {
	t.Helper()

	f := &fakeConsul{kv: map[string]fakeKV{}, sessions: map[string]string{}, failRenew: map[string]bool{}}

	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/kv/{key...}", f.put)
	mux.HandleFunc("GET /v1/kv/{key...}", f.get)
	mux.HandleFunc("PUT /v1/session/create", f.create)
	mux.HandleFunc("PUT /v1/session/renew/{id}", f.renew)
	mux.HandleFunc("PUT /v1/session/destroy/{id}", f.destroy)
	mux.HandleFunc("GET /v1/session/info/{id}", f.info)
	mux.HandleFunc("PUT /v1/agent/service/register", f.register)
	mux.HandleFunc("PUT /v1/agent/service/deregister/{id}", func(http.ResponseWriter, *http.Request) {
		f.with(func(f *fakeConsul) { f.deregisters++ })
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c, err := api.NewClient(&api.Config{Address: srv.Listener.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}

	return f, c
}

func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (f *fakeConsul) put(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	key := r.PathValue("key")

	f.mu.Lock()
	defer f.mu.Unlock()

	if r.URL.Query().Get("cas") == "0" {
		if f.manifestOnCAS != nil {
			f.kv[key] = fakeKV{value: f.manifestOnCAS}
			f.manifestOnCAS = nil
		}

		if _, exists := f.kv[key]; exists {
			reply(w, false)

			return
		}
	}

	if !r.URL.Query().Has("acquire") {
		f.kv[key] = fakeKV{value: body}

		reply(w, true)

		return
	}

	session := r.URL.Query().Get("acquire")

	switch cur, held := f.kv[key]; {
	case f.failAcquires > 0:
		f.failAcquires--

		http.Error(w, "injected", http.StatusInternalServerError)
	case f.sessions[session] == "":
		http.Error(w, "invalid session "+session, http.StatusInternalServerError)
	case held && cur.session != "" && cur.session != session:
		reply(w, false)
	default:
		f.kv[key] = fakeKV{value: body, session: session}

		reply(w, true)
	}
}

func (f *fakeConsul) get(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	key := r.PathValue("key")

	kv, ok := f.kv[key]
	if !ok {
		http.NotFound(w, r)

		return
	}

	reply(w, []api.KVPair{{Key: key, Value: kv.value, Session: kv.session}})
}

func (f *fakeConsul) create(w http.ResponseWriter, r *http.Request) {
	var body map[string]any

	_ = json.NewDecoder(r.Body).Decode(&body)
	name, _ := body["Name"].(string)

	reply(w, map[string]string{"ID": f.newSession(name)})
}

func (f *fakeConsul) newSession(name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.seq++
	id := fmt.Sprintf("s%d", f.seq)
	f.sessions[id] = name
	f.events = append(f.events, "create "+id)

	return id
}

func (f *fakeConsul) renew(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.renews++
	id := r.PathValue("id")

	switch {
	case f.failRenews > 0 || f.failRenew[id]:
		f.failRenews = max(f.failRenews-1, 0)

		http.Error(w, "injected", http.StatusInternalServerError)
	case f.sessions[id] == "":
		http.NotFound(w, r)
	default:
		reply(w, []api.SessionEntry{{ID: id, Name: f.sessions[id]}})
	}
}

func (f *fakeConsul) destroy(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	id := r.PathValue("id")
	f.events = append(f.events, "destroy "+id)
	f.invalidate(id)
	reply(w, true)
}

// invalidate drops a session and, as Behavior=delete does, its keys; f.mu is held.
func (f *fakeConsul) invalidate(id string) {
	delete(f.sessions, id)

	for key, kv := range f.kv {
		if kv.session == id {
			delete(f.kv, key)
		}
	}
}

func (f *fakeConsul) info(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	id := r.PathValue("id")
	if name := f.sessions[id]; name != "" {
		reply(w, []api.SessionEntry{{ID: id, Name: name}})

		return
	}

	reply(w, []api.SessionEntry{})
}

func (f *fakeConsul) register(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.failRegisters > 0 {
		f.failRegisters--

		http.Error(w, "injected", http.StatusInternalServerError)

		return
	}

	var reg api.AgentServiceRegistration
	if err := json.NewDecoder(r.Body).Decode(&reg); err == nil {
		f.registrations = append(f.registrations, reg)
	}
}

func (f *fakeConsul) with(fn func(f *fakeConsul)) {
	f.mu.Lock()
	defer f.mu.Unlock()

	fn(f)
}

// hold makes a foreign session named name lock key.
func (f *fakeConsul) hold(key, name string) string {
	id := f.newSession(name)
	f.with(func(f *fakeConsul) { f.kv[key] = fakeKV{value: []byte("other"), session: id} })

	return id
}

func (f *fakeConsul) holderOf(key string) string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.kv[key].session
}

func (f *fakeConsul) eventLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.events)
}

func (f *fakeConsul) live() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	ids := make([]string, 0, len(f.sessions))
	for id := range f.sessions {
		ids = append(ids, id)
	}

	slices.Sort(ids)

	return ids
}

func (f *fakeConsul) renewCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.renews
}

func stateKey(id consul.Identity) string {
	return "backplane/services/" + id.Service + "/instances/" + id.Instance
}

func established(f *fakeConsul, p *consul.Presence, key string) func() bool {
	return func() bool { s := p.Session(); return s != "" && f.holderOf(key) == s }
}

var testID = consul.Identity{Service: "svc", Version: "1.0.0", Instance: "svc-1", Address: "127.0.0.1", PlatformPort: 1}

func TestRenewFailureTolerated(t *testing.T) {
	t.Parallel()

	f, c := newFake(t)
	key := stateKey(testID)
	p := start(t, consul.Params{Client: c, Identity: testID})

	eventually(t, "established", established(f, p, key))

	session := p.Session()
	base := f.renewCount()

	// A few failures stay well inside renewGiveUp.
	f.with(func(f *fakeConsul) { f.failRenews = 3 })
	eventually(t, "renewed through failures", func() bool { return f.renewCount() >= base+8 })

	if got := f.eventLog(); !slices.Equal(got, []string{"create " + session}) || p.Session() != session {
		t.Fatalf("session re-established on transient renew failures: %v, now %s", got, p.Session())
	}
}

// Renew failing past renewGiveUp re-establishes, destroying the old session
// before the new one is created.
func TestRenewGiveUpDestroysOwnSessionFirst(t *testing.T) {
	t.Parallel()

	f, c := newFake(t)
	key := stateKey(testID)
	p := start(t, consul.Params{Client: c, Identity: testID})

	eventually(t, "established", established(f, p, key))

	old := p.Session()

	f.with(func(f *fakeConsul) { f.failRenew[old] = true })

	eventually(t, "re-established", func() bool { return p.Session() != old && established(f, p, key)() })

	want := []string{"create " + old, "destroy " + old, "create " + p.Session()}
	if got := f.eventLog(); !slices.Equal(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
}

func TestSessionLostReestablishes(t *testing.T) {
	t.Parallel()

	f, c := newFake(t)
	key := stateKey(testID)
	p := start(t, consul.Params{Client: c, Identity: testID})

	eventually(t, "established", established(f, p, key))

	old := p.Session()

	f.with(func(f *fakeConsul) { f.invalidate(old) })

	eventually(t, "re-established", func() bool { return p.Session() != old && established(f, p, key)() })
}

// A session created by a failed establish is destroyed at once.
func TestFailedEstablishDestroysSession(t *testing.T) {
	t.Parallel()

	for name, fail := range map[string]func(f *fakeConsul){
		"state write": func(f *fakeConsul) { f.failAcquires = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f, c := newFake(t)
			key := stateKey(testID)

			f.with(fail)

			p := start(t, consul.Params{Client: c, Identity: testID, Register: true})

			eventually(t, "established on the retry", func() bool {
				return p.Session() == "s2" && established(f, p, key)()
			})

			want := []string{"create s1", "destroy s1", "create s2"}
			if got := f.eventLog(); !slices.Equal(got, want) || p.Session() != "s2" {
				t.Fatalf("events %v, want %v", got, want)
			}

			if live := f.live(); !slices.Equal(live, []string{"s2"}) {
				t.Fatalf("orphan sessions: %v", live)
			}
		})
	}
}

// A failed catalog registration keeps the session: registration is retried
// on the next renew, the instance state stays where it is.
func TestFailedRegistrationKeepsSession(t *testing.T) {
	t.Parallel()

	f, c := newFake(t)
	key := stateKey(testID)

	f.with(func(f *fakeConsul) { f.failRegisters = 1 })

	p := start(t, consul.Params{Client: c, Identity: testID, Register: true})

	eventually(t, "established", established(f, p, key))
	eventually(t, "registered on a later attempt", func() bool { return len(f.registered()) == 1 })

	if got := f.eventLog(); !slices.Equal(got, []string{"create s1"}) || p.Session() != "s1" {
		t.Fatalf("events %v, session %s: the session must survive a failed registration", got, p.Session())
	}
}

// The first establish takes over from a crashed predecessor: same instance
// id, another incarnation.
func TestFirstEstablishEvictsPredecessor(t *testing.T) {
	t.Parallel()

	f, c := newFake(t)
	key := stateKey(testID)
	stale := f.hold(key, testID.Instance+"#crashed")

	p := start(t, consul.Params{Client: c, Identity: testID})

	eventually(t, "established", established(f, p, key))

	if slices.Contains(f.live(), stale) {
		t.Fatal("stale session not evicted")
	}
}

// A foreign holder (not an incarnation of this instance) is never evicted.
func TestForeignHolderNotEvicted(t *testing.T) {
	t.Parallel()

	f, c := newFake(t)
	key := stateKey(testID)
	foreign := f.hold(key, testID.Instance+"-other#x")

	start(t, consul.Params{Client: c, Identity: testID})

	eventually(t, "a few attempts", func() bool { return len(f.eventLog()) >= 6 })

	if f.holderOf(key) != foreign {
		t.Fatal("foreign session evicted")
	}
}

// After the first establish, another incarnation holding the key is a live
// duplicate: reported, not evicted, retried.
func TestDuplicateNotEvictedAfterFirstEstablish(t *testing.T) {
	t.Parallel()

	f, c := newFake(t)
	key := stateKey(testID)
	logs := &logBuf{}
	p := start(t, consul.Params{Client: c, Identity: testID, Log: xlog.NewJSON(xlog.WithWriter(logs))})

	eventually(t, "established", established(f, p, key))

	// The duplicate started, evicted us and took the key.
	var dup string

	f.with(func(f *fakeConsul) {
		f.invalidate(p.Session())
		f.seq++
		dup = fmt.Sprintf("s%d", f.seq)
		f.sessions[dup] = testID.Instance + "#duplicate"
		f.kv[key] = fakeKV{value: []byte("dup"), session: dup}
	})

	eventually(t, "duplicate reported", func() bool { return logs.has(`"duplicate instance id"`, "error") })
	eventually(t, "retried", func() bool { return len(f.eventLog()) >= 12 })

	if f.holderOf(key) != dup || !slices.Contains(f.live(), dup) {
		t.Fatal("live duplicate evicted")
	}

	if live := f.live(); len(live) > 2 {
		t.Fatalf("orphan sessions: %v", live)
	}
}

func TestDirtyStateRetried(t *testing.T) {
	t.Parallel()

	f, c := newFake(t)
	key := stateKey(testID)
	cfg := &fakeConfig{values: []byte("v1")}
	p := start(t, consul.Params{Client: c, Identity: testID, Config: cfg})

	eventually(t, "established", established(f, p, key))

	config := func() string {
		var raw []byte

		f.with(func(f *fakeConsul) { raw = f.kv[key].value })

		var st backplanev1.InstanceState
		if err := proto.Unmarshal(raw, &st); err != nil {
			return ""
		}

		return string(st.GetConfig())
	}

	if got := config(); got != "v1" {
		t.Fatalf("config %q", got)
	}

	f.with(func(f *fakeConsul) { f.failAcquires = 3 })
	cfg.set("v2")

	eventually(t, "state republished after failed writes", func() bool { return config() == "v2" })
}

func TestStopBeforeLoopRan(t *testing.T) {
	t.Parallel()

	_, c := newFake(t)

	p, err := consul.New(consul.Params{Client: c, Log: testlog.Discard(), Identity: testID, Manifest: &backplanev1.Manifest{}})
	if err != nil {
		t.Fatal(err)
	}

	if err := p.Start(context.Background(), dropped{}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := p.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

// Runs against a real Consul: BACKPLANE_TEST_CONSUL=localhost:8500 (make up).
func client(t *testing.T) *api.Client {
	t.Helper()

	addr := os.Getenv("BACKPLANE_TEST_CONSUL")
	if addr == "" {
		t.Skip("BACKPLANE_TEST_CONSUL not set")
	}

	c, err := api.NewClient(&api.Config{Address: addr})
	if err != nil {
		t.Fatal(err)
	}

	return c
}

func waitKey(t *testing.T, c *api.Client, key string, present bool) *api.KVPair {
	t.Helper()

	var found *api.KVPair

	eventually(t, fmt.Sprintf("key %s present=%v", key, present), func() bool {
		kv, _, err := c.KV().Get(key, nil)
		found = kv

		return err == nil && (kv != nil) == present
	})

	return found
}

//nolint:paralleltest // shares one Consul with other packages' tests
func TestPresenceLifecycle(t *testing.T) {
	c := client(t)
	id := consul.Identity{
		Service: "presence-test", Version: "1.2.3", Instance: "presence-test-1", Address: "127.0.0.1", PlatformPort: 1,
	}
	manifest := &backplanev1.Manifest{Service: id.Service, Version: id.Version}

	// A leftover entry of an aborted run would satisfy "in catalog" below.
	_ = c.Agent().ServiceDeregister(id.Instance)

	t.Cleanup(func() {
		_, _ = c.KV().DeleteTree("backplane/services/presence-test/", nil)
		_ = c.Agent().ServiceDeregister(id.Instance)
	})

	p, err := consul.New(consul.Params{Client: c, Log: testlog.Discard(), Identity: id, Manifest: manifest, Register: true})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	if err := p.Start(ctx, group{ctx}); err != nil {
		t.Fatal(err)
	}

	if err := p.Register(ctx); err != nil {
		t.Fatal(err)
	}

	raw := waitKey(t, c, "backplane/services/presence-test/manifests/1.2.3", true)

	var m backplanev1.Manifest
	if err := proto.Unmarshal(raw.Value, &m); err != nil || m.GetService() != "presence-test" {
		t.Fatalf("manifest: %v %v", &m, err)
	}

	state := waitKey(t, c, stateKey(id), true)
	if state.Session == "" {
		t.Fatal("state not bound to a session")
	}

	if info, _, err := c.Session().Info(state.Session, nil); err != nil || info == nil || info.Name != p.SessionName() {
		t.Fatalf("session %v %v, want name %s", info, err, p.SessionName())
	}

	// Registration follows the state write.
	eventually(t, "in catalog", func() bool {
		services, _, err := c.Catalog().Service("presence-test", "", nil)

		return err == nil && len(services) == 1
	})

	cancel()

	if err := p.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	waitKey(t, c, stateKey(id), false)

	eventually(t, "out of catalog", func() bool {
		services, _, err := c.Catalog().Service("presence-test", "", nil)
		return err == nil && len(services) == 0
	})
}

// A crashed instance leaves its session; the restarted one with the same id
// must take over immediately.
//
//nolint:paralleltest // shares one Consul with other packages' tests
func TestRestartTakesOverStaleSession(t *testing.T) {
	c := client(t)
	id := consul.Identity{
		Service: "presence-restart", Version: "1.0.0", Instance: "presence-restart-1", Address: "127.0.0.1", PlatformPort: 1,
	}
	key := stateKey(id)

	t.Cleanup(func() { _, _ = c.KV().DeleteTree("backplane/services/presence-restart/", nil) })

	// An earlier incarnation created by this SDK: short lock delay.
	stale, _, err := c.Session().Create(&api.SessionEntry{
		Name: id.Instance + "#crashed", TTL: "60s", Behavior: api.SessionBehaviorDelete, LockDelay: time.Millisecond,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _, _ = c.Session().Destroy(stale, nil) })

	if ok, _, err := c.KV().Acquire(&api.KVPair{Key: key, Value: []byte("old"), Session: stale}, nil); err != nil || !ok {
		t.Fatalf("seed lock: %v %v", ok, err)
	}

	p := start(t, consul.Params{Client: c, Identity: id})

	eventually(t, "stale session taken over", func() bool {
		kv, _, _ := c.KV().Get(key, nil)

		return kv != nil && kv.Session != "" && kv.Session == p.Session()
	})
}

// Two live processes with one instance id: the newer takes over on its
// first establish; the older one then reports the duplicate and does not
// take the key back.
//
//nolint:paralleltest // shares one Consul with other packages' tests
func TestDuplicateInstanceNotEvicted(t *testing.T) {
	c := client(t)
	id := consul.Identity{
		Service: "presence-dup", Version: "1.0.0", Instance: "presence-dup-1", Address: "127.0.0.1", PlatformPort: 1,
	}
	key := stateKey(id)

	t.Cleanup(func() { _, _ = c.KV().DeleteTree("backplane/services/presence-dup/", nil) })

	logs := &logBuf{}
	older := start(t, consul.Params{Client: c, Identity: id, Log: xlog.NewJSON(xlog.WithWriter(logs))})

	eventually(t, "older established", func() bool {
		kv, _, _ := c.KV().Get(key, nil)

		return kv != nil && kv.Session != "" && kv.Session == older.Session()
	})

	newer := start(t, consul.Params{Client: c, Identity: id})

	eventually(t, "newer took over", func() bool {
		kv, _, _ := c.KV().Get(key, nil)

		return kv != nil && kv.Session != "" && kv.Session == newer.Session()
	})
	eventually(t, "duplicate reported", func() bool { return logs.has(`"duplicate instance id"`, "error") })

	time.Sleep(300 * time.Millisecond) // several more attempts of the older one

	if kv, _, err := c.KV().Get(key, nil); err != nil || kv == nil || kv.Session != newer.Session() {
		t.Fatalf("key taken back from the newer process: %v %v", kv, err)
	}
}

// A duplicate that lost the state key leaves the catalog entry to the
// process that holds it.
//
//nolint:paralleltest // shares one Consul with other packages' tests
func TestDuplicateStopKeepsHoldersRegistration(t *testing.T) {
	c := client(t)
	id := consul.Identity{
		Service: "presence-dupreg", Version: "1.0.0", Instance: "presence-dupreg-1",
		Address: "127.0.0.1", PlatformPort: 1,
	}
	key := stateKey(id)

	t.Cleanup(func() {
		_, _ = c.KV().DeleteTree("backplane/services/presence-dupreg/", nil)
		_ = c.Agent().ServiceDeregister(id.Instance)
	})

	older, err := consul.New(consul.Params{
		Client: c, Identity: id, Register: true, Log: testlog.Discard(),
		Manifest: &backplanev1.Manifest{Service: id.Service, Version: id.Version},
	})
	if err != nil {
		t.Fatal(err)
	}

	older.UseFastTiming()

	ctx, cancel := context.WithCancel(context.Background())
	if err := older.Register(ctx); err != nil {
		t.Fatal(err)
	}

	if err := older.Start(ctx, group{ctx}); err != nil {
		t.Fatal(err)
	}

	eventually(t, "older established", func() bool {
		kv, _, _ := c.KV().Get(key, nil)

		return kv != nil && kv.Session == older.Session()
	})

	newer := start(t, consul.Params{Client: c, Identity: id, Register: true})

	eventually(t, "newer took over", func() bool {
		kv, _, _ := c.KV().Get(key, nil)

		return kv != nil && kv.Session == newer.Session()
	})

	cancel()

	if err := older.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	services, err := c.Agent().Services()
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := services[id.Instance]; !ok {
		t.Fatal("the stopped duplicate deregistered the holder's catalog entry")
	}
}
