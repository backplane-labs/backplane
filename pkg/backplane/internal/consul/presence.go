// Package consul keeps a service present in Consul: manifest per version,
// instance state under a TTL session (it disappears with the instance) and
// the catalog registration. The state is published from before the author's
// tree starts (phase starting) so a service waiting for its dependencies is
// visible; the catalog registration follows only once it serves. Consul
// being absent never fails the service.
package consul

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	retry "github.com/cenkalti/backoff/v5"
	"github.com/hashicorp/consul/api"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/gopherex/xlog"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/pkg/backplane/internal/backoff"
	"github.com/gopherex/backplane/pkg/backplane/internal/configrt"
	"github.com/gopherex/backplane/pkg/backplane/internal/metrics"
	"github.com/gopherex/backplane/pkg/backplane/internal/node"
)

var (
	// ErrStateHeld: the instance state key is locked by a session that is
	// not an incarnation of this instance, or is still in its lock delay.
	ErrStateHeld = errors.New("consul: instance state held")
	// ErrDuplicateInstance: another live process runs with this instance id.
	ErrDuplicateInstance = errors.New("consul: duplicate instance id")
	// ErrManifestConflict: this version already has different declarations.
	ErrManifestConflict = errors.New("consul: different manifest already published for this version")

	errSessionLost     = errors.New("consul: session expired")
	errManifestMissing = errors.New("consul: manifest disappeared during publication")
)

const (
	servicesPrefix = "backplane/services/"
	// Defaults of the catalog health check and the session.
	defaultCheckInterval   = 10 * time.Second
	defaultCheckTimeout    = 5 * time.Second
	defaultDeregisterAfter = time.Minute
	// Consul accepts session TTLs from 10s to 24h.
	minSessionTTL = 10 * time.Second
	maxSessionTTL = 24 * time.Hour
	// lockDelay: Consul's default 15s would block a restarted instance with
	// the same id from publishing its state.
	lockDelay = time.Millisecond
	// incarnationSep separates the instance id from the incarnation in the
	// session name.
	incarnationSep = "#"
	incarnationLen = 10

	defaultSessionTTL = 30 * time.Second
	// Renew three times per TTL; after TTL/2 of failed renews the session is
	// at risk and presence is re-established instead.
	renewsPerTTL   = 3
	renewGiveUpDiv = 2
	attemptTimeout = 20 * time.Second
	callTimeout    = 5 * time.Second
	establishFloor = 2 * time.Second
	establishCeil  = time.Minute
	renewFloor     = time.Second
	renewCeil      = 4 * time.Second
)

// timing holds the presence clocks; tests shorten them.
type timing struct {
	ttl         time.Duration
	renewEvery  time.Duration
	renewGiveUp time.Duration  // failing renews tolerated before re-establish
	attempt     time.Duration  // bound on one establish attempt
	call        time.Duration  // bound on one renew, state write or cleanup
	establish   backoff.Policy // between establish attempts
	renew       backoff.Policy // between renew attempts
}

func defaultTiming(ttl time.Duration) timing {
	switch {
	case ttl == 0:
		ttl = defaultSessionTTL
	case ttl < minSessionTTL:
		ttl = minSessionTTL
	case ttl > maxSessionTTL:
		ttl = maxSessionTTL
	}

	return timing{
		ttl:         ttl,
		renewEvery:  ttl / renewsPerTTL,
		renewGiveUp: ttl / renewGiveUpDiv,
		attempt:     attemptTimeout,
		call:        callTimeout,
		establish:   backoff.Policy{Min: establishFloor, Max: establishCeil},
		renew:       backoff.Policy{Min: renewFloor, Max: renewCeil},
	}
}

// Identity is how the instance presents itself.
type Identity struct {
	Service      string
	Version      string
	Instance     string
	Address      string
	PlatformPort uint16
	PublicPort   uint16 // 0 when the service has no managed public routes
	Commit       string
}

// ConfigState is the dynamic configuration seen by the presence.
type ConfigState interface {
	Effective() configrt.Effective
	OnChange(fn func())
}

// Check is the catalog registration's health check; zero fields take the
// defaults (10s, 5s, 1m).
type Check struct {
	Interval        time.Duration
	Timeout         time.Duration
	DeregisterAfter time.Duration
}

// Params of New.
type Params struct {
	Client   *api.Client
	Log      *xlog.Logger
	Identity Identity
	Manifest *backplanev1.Manifest
	// Register in the catalog; false when the deployment registers.
	Register bool
	// Tags of the catalog registration.
	Tags  []string
	Check Check
	// SessionTTL of the session holding the instance state; 0 is 30s,
	// clamped to Consul's 10s..24h. Renewed every TTL/3; renewals failing
	// for TTL/2 re-establish the presence.
	SessionTTL time.Duration
	// Config may be nil.
	Config ConfigState
	// Nodes reports the author's dependencies; may be nil. Read on every
	// state write and renew tick: a change is published within a renew
	// interval, or at once after Changed.
	Nodes func() []*backplanev1.NodeStatus
	// Transports reports NATS, Temporal and OTLP; may be nil. Consul is
	// added by the presence itself. Read like Nodes.
	Transports func() []*backplanev1.TransportStatus
}

// Presence is a lifecycle component.
type Presence struct {
	client *api.Client
	log    *xlog.Logger
	id     Identity
	// name of this process's sessions: instance id and a random incarnation,
	// so a live duplicate is told apart from a crashed predecessor.
	name       string
	manifest   *backplanev1.Manifest
	raw        []byte // manifest, deterministic
	register   bool
	tags       []string
	check      Check
	config     ConfigState
	nodes      func() []*backplanev1.NodeStatus
	transports func() []*backplanev1.TransportStatus
	timing     timing
	changed    chan struct{}
	done       chan struct{}
	started    time.Time // set by Start before the loop runs
	phase      atomic.Int32

	mu        sync.Mutex
	session   string
	running   bool
	stopped   bool
	connected bool   // a session is established
	written   []byte // last instance state written

	// writeMu serializes instance state writes: the loop and Deregister.
	writeMu sync.Mutex

	// regMu guards the catalog registration; held across register calls so
	// Deregister never races a registration in flight.
	regMu      sync.Mutex
	serving    bool
	registered bool
}

// New creates the presence; nothing is sent until Start.
func New(p Params) (*Presence, error) {
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(p.Manifest)
	if err != nil {
		return nil, fmt.Errorf("consul: marshal manifest: %w", err)
	}

	check := p.Check
	check.Interval = or(check.Interval, defaultCheckInterval)
	check.Timeout = or(check.Timeout, defaultCheckTimeout)
	check.DeregisterAfter = or(check.DeregisterAfter, defaultDeregisterAfter)

	presence := &Presence{
		client: p.Client, log: p.Log, id: p.Identity, manifest: p.Manifest, raw: raw,
		register: p.Register, tags: slices.Clone(p.Tags), check: check,
		config: p.Config, nodes: p.Nodes, transports: p.Transports,
		name:    p.Identity.Instance + incarnationSep + rand.Text()[:incarnationLen],
		timing:  defaultTiming(p.SessionTTL),
		changed: make(chan struct{}, 1), done: make(chan struct{}),
	}
	presence.phase.Store(int32(backplanev1.InstancePhase_INSTANCE_PHASE_STARTING))

	return presence, nil
}

func or(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}

	return def
}

// Start publishes the manifest and the instance state (phase starting) in
// the background, ahead of the author's tree; the catalog registration
// waits for Register. Retries never block or fail the service.
func (p *Presence) Start(_ context.Context, g node.Group) error {
	p.started = time.Now()
	if p.config != nil {
		p.config.OnChange(p.notify)
	}

	g.Go(func(ctx context.Context) error {
		if !p.enter() {
			return nil
		}

		defer close(p.done)

		return p.run(ctx)
	})

	return nil
}

// Stop waits for the presence loop to exit (the node has already cancelled
// it), then deregisters and destroys the session so the state key
// disappears. Leaving is best effort: Consul errors are logged.
func (p *Presence) Stop(ctx context.Context) error {
	if !p.leave() {
		return nil // the loop never ran: nothing was sent
	}

	select {
	case <-p.done:
	case <-ctx.Done():
		return fmt.Errorf("consul: waiting for presence loop: %w", ctx.Err())
	}

	errs := []error{p.deregister(ctx), p.dropSession(ctx)}

	if err := errors.Join(errs...); err != nil {
		p.log.Warn("consul leave", xlog.Err(err))
	}

	p.setConnected(context.WithoutCancel(ctx), false)

	return nil
}

// Register marks the instance serving: the state says so and the loop
// registers in the catalog (when the SDK registers), retrying in the
// background. Call it when the instance takes traffic.
func (p *Presence) Register(context.Context) error {
	p.regMu.Lock()
	p.serving = true
	p.phase.Store(int32(backplanev1.InstancePhase_INSTANCE_PHASE_SERVING))
	p.regMu.Unlock()

	p.notify()

	return nil
}

// Deregister marks the instance stopping: it leaves the catalog at once and
// the state says so, while the session and the state live on until Stop.
// Call it when the instance stops taking traffic. Best effort: Consul
// errors are logged.
func (p *Presence) Deregister(ctx context.Context) error {
	p.phase.Store(int32(backplanev1.InstancePhase_INSTANCE_PHASE_STOPPING))

	if err := p.deregister(ctx); err != nil {
		p.log.Warn("consul deregister", xlog.Err(err))
	}

	if p.currentSession() != "" {
		p.publish(ctx)
	}

	return nil
}

// Changed republishes the state now: nodes or transports changed.
func (p *Presence) Changed() { p.notify() }

// deregister leaves the catalog if this process registered. The entry is
// keyed by instance id: a live duplicate holding the state key owns it too,
// so only the holder deregisters.
func (p *Presence) deregister(ctx context.Context) error {
	p.regMu.Lock()
	defer p.regMu.Unlock()

	was := p.registered
	p.serving, p.registered = false, false

	if !was || !p.holdsState(ctx) {
		return nil
	}

	if err := p.client.Agent().ServiceDeregisterOpts(p.id.Instance, query(ctx)); err != nil {
		return fmt.Errorf("deregister: %w", err)
	}

	return nil
}

// ensureRegistered registers in the catalog while serving; force
// re-registers (after a new session: the agent may have lost the entry).
func (p *Presence) ensureRegistered(ctx context.Context, force bool) error {
	p.regMu.Lock()
	defer p.regMu.Unlock()

	if !p.register || !p.serving || (p.registered && !force) {
		return nil
	}

	if err := p.registerService(ctx); err != nil {
		return err
	}

	if !p.registered {
		p.log.Info("registered in consul catalog")
	}

	p.registered = true

	return nil
}

// holdsState reports whether our session holds the instance state key; an
// unreachable Consul counts as holding it (best-effort leave).
func (p *Presence) holdsState(ctx context.Context) bool {
	kv, _, err := p.client.KV().Get(p.key("instances/"+p.id.Instance), query(ctx))
	if err != nil || kv == nil || kv.Session == "" {
		return true // unknown or unheld: this process registered last, it leaves
	}

	return kv.Session == p.currentSession()
}

// enter marks the loop running unless Stop came first.
func (p *Presence) enter() bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.stopped {
		return false
	}

	p.running = true

	return true
}

// leave marks the presence stopped and reports whether the loop ran.
func (p *Presence) leave() bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.stopped = true

	return p.running
}

func (p *Presence) currentSession() string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.session
}

func (p *Presence) setSession(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.session = id
}

func (p *Presence) setConnected(ctx context.Context, connected bool) {
	p.mu.Lock()
	changed := p.connected != connected
	p.connected = connected
	p.mu.Unlock()

	if changed {
		metrics.TransportConnected(ctx, "consul", connected)
	}
}

func (p *Presence) notify() {
	select {
	case p.changed <- struct{}{}:
	default:
	}
}

// run establishes presence with backoff and maintains it until ctx ends.
// Only the first establish of the process may evict another incarnation of
// this instance: that one is a crashed predecessor; later it is a live
// duplicate.
func (p *Presence) run(ctx context.Context) error {
	for first := true; ctx.Err() == nil; first = false {
		err := backoff.Retry(ctx, p.timing.establish, func(ctx context.Context) error {
			err := p.establish(ctx, first)
			if errors.Is(err, ErrManifestConflict) {
				return retry.Permanent(err)
			}

			return err
		}, p.retrying)
		if ctx.Err() != nil {
			return nil //nolint:nilerr // cancellation is normal shutdown, not a node failure
		}

		if err != nil {
			return fmt.Errorf("consul presence: %w", err)
		}

		p.setConnected(ctx, true)
		p.log.Info("instance state published in consul", xlog.String("session", p.currentSession()))
		p.maintain(ctx)
		p.setConnected(ctx, false)
	}

	return nil
}

func (p *Presence) retrying(err error, in time.Duration) {
	if errors.Is(err, ErrDuplicateInstance) {
		p.log.Error("duplicate instance id", xlog.Err(err), xlog.Duration("in", in))

		return
	}

	p.log.Warn("consul unavailable, retrying", xlog.Err(err), xlog.Duration("in", in))
}

func (p *Presence) key(suffix string) string { return servicesPrefix + p.id.Service + "/" + suffix }

func (p *Presence) establish(ctx context.Context, first bool) error {
	ctx, cancel := context.WithTimeout(ctx, p.timing.attempt)
	defer cancel()

	// The previous session of this process (lost, or kept by a failed
	// attempt) goes first: its lock would otherwise block the new one.
	if err := p.dropSession(ctx); err != nil {
		return err
	}

	if err := p.putManifest(ctx); err != nil {
		return err
	}

	session, _, err := p.client.Session().Create(&api.SessionEntry{
		Name: p.name, TTL: p.timing.ttl.String(), Behavior: api.SessionBehaviorDelete, LockDelay: lockDelay,
	}, write(ctx))
	if err != nil {
		return fmt.Errorf("session: %w", err)
	}

	p.setSession(session)

	if err := p.writeState(ctx, first); err != nil {
		return p.abandon(ctx, err)
	}

	// The catalog registration is independent of the session: a failure
	// here keeps the session and is retried on every renew.
	if err := p.ensureRegistered(ctx, true); err != nil && ctx.Err() == nil {
		p.log.Warn("consul catalog registration, retrying on next renew", xlog.Err(err))
	}

	return nil
}

// putManifest creates an immutable manifest for every version, including
// versions with build metadata. A conflict prevents publishing instance state.
func (p *Presence) putManifest(ctx context.Context) error {
	key := p.key("manifests/" + p.id.Version)

	existing, _, err := p.client.KV().Get(key, query(ctx))
	if err != nil {
		return fmt.Errorf("manifest: %w", err)
	}

	if existing != nil {
		return p.checkManifest(key, existing.Value)
	}

	ok, _, err := p.client.KV().CAS(&api.KVPair{Key: key, Value: p.raw}, write(ctx))
	if err != nil {
		return fmt.Errorf("manifest: %w", err)
	}

	if !ok {
		return p.recheckManifest(ctx, key)
	}

	return nil
}

func (p *Presence) recheckManifest(ctx context.Context, key string) error {
	existing, _, err := p.client.KV().Get(key, query(ctx))
	if err != nil {
		return fmt.Errorf("manifest: %w", err)
	}

	if existing == nil {
		return fmt.Errorf("%w: %s", errManifestMissing, key)
	}

	return p.checkManifest(key, existing.Value)
}

func (p *Presence) checkManifest(key string, raw []byte) error {
	if p.sameManifest(raw) {
		return nil
	}

	return fmt.Errorf("%w: %s; publish this build under a new version", ErrManifestConflict, key)
}

func (p *Presence) sameManifest(raw []byte) bool {
	if bytes.Equal(raw, p.raw) {
		return true
	}

	var m backplanev1.Manifest

	return proto.Unmarshal(raw, &m) == nil && proto.Equal(&m, p.manifest)
}

// abandon destroys the session of a failed establish so it does not linger
// until its TTL; if that fails too, the next attempt destroys it first.
func (p *Presence) abandon(ctx context.Context, cause error) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), p.timing.call)
	defer cancel()

	return errors.Join(cause, p.dropSession(ctx))
}

// dropSession destroys this process's current session, if any.
func (p *Presence) dropSession(ctx context.Context) error {
	session := p.currentSession()
	if session == "" {
		return nil
	}

	if _, err := p.client.Session().Destroy(session, write(ctx)); err != nil {
		return fmt.Errorf("destroy session %s: %w", session, err)
	}

	p.setSession("")

	return nil
}

func (p *Presence) registerService(ctx context.Context) error {
	if !p.register {
		return nil
	}

	reg := &api.AgentServiceRegistration{
		ID:      p.id.Instance,
		Name:    p.id.Service,
		Address: p.id.Address,
		Port:    int(p.id.PublicPort),
		Tags:    p.tags,
		Check: &api.AgentServiceCheck{
			GRPC:                           net.JoinHostPort(p.id.Address, strconv.Itoa(int(p.id.PlatformPort))),
			Interval:                       p.check.Interval.String(),
			Timeout:                        p.check.Timeout.String(),
			DeregisterCriticalServiceAfter: p.check.DeregisterAfter.String(),
		},
	}
	if err := p.client.Agent().ServiceRegisterOpts(reg, api.ServiceRegisterOpts{}.WithContext(ctx)); err != nil {
		return fmt.Errorf("register: %w", err)
	}

	return nil
}

// maintain renews the session, republishes state on change and keeps the
// catalog registration while serving; returns when ctx ends or the session
// is lost. A state write or registration that fails stays pending and is
// retried on every renew tick until it succeeds. Every tick also compares
// the state with the last written one: nodes and transports are polled.
func (p *Presence) maintain(ctx context.Context) {
	ticker := time.NewTicker(p.timing.renewEvery)
	defer ticker.Stop()

	dirty := false

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := p.renew(ctx); err != nil {
				if ctx.Err() == nil {
					p.log.Warn("consul session lost, re-establishing", xlog.Err(err))
				}

				return
			}

			dirty = dirty || p.stale()
		case <-p.changed:
			dirty = true
		}

		if dirty {
			dirty = !p.publish(ctx)
		}

		p.syncRegistration(ctx)
	}
}

// stale reports whether the state differs from the last one written.
func (p *Presence) stale() bool {
	raw, err := p.state()
	if err != nil {
		return false
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	return !bytes.Equal(raw, p.written)
}

func (p *Presence) syncRegistration(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, p.timing.call)
	defer cancel()

	if err := p.ensureRegistered(ctx, false); err != nil && ctx.Err() == nil {
		p.log.Warn("consul catalog registration, retrying on next renew", xlog.Err(err))
	}
}

// renew renews the session, retrying failures for up to renewGiveUp — well
// inside the TTL — before declaring it lost. A session Consul no longer
// knows is lost at once.
func (p *Presence) renew(ctx context.Context) error {
	session := p.currentSession()

	ctx, cancel := context.WithTimeout(ctx, p.timing.renewGiveUp)
	defer cancel()

	lost := false

	err := backoff.Retry(ctx, p.timing.renew, func(ctx context.Context) error {
		ctx, stop := context.WithTimeout(ctx, p.timing.call)
		defer stop()

		entry, _, err := p.client.Session().Renew(session, write(ctx))
		if err != nil {
			return fmt.Errorf("renew session %s: %w", session, err)
		}

		lost = entry == nil

		return nil
	}, func(err error, in time.Duration) {
		p.log.Warn("consul session renew failed, retrying", xlog.Err(err), xlog.Duration("in", in))
	})

	switch {
	case err != nil:
		return fmt.Errorf("renew: %w", err)
	case lost:
		return fmt.Errorf("%w: %s", errSessionLost, session)
	}

	return nil
}

// publish writes the instance state; false when it has to be retried.
func (p *Presence) publish(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, p.timing.call)
	defer cancel()

	if err := p.writeState(ctx, false); err != nil {
		if ctx.Err() == nil {
			p.log.Warn("instance state, retrying on next renew", xlog.Err(err))
		}

		return false
	}

	return true
}

// state is the instance state: identity, phase, the applied configuration
// with the revision it came from and the last rejected one, dependencies
// and transports.
func (p *Presence) state() ([]byte, error) {
	state := &backplanev1.InstanceState{
		Id:           p.id.Instance,
		Service:      p.id.Service,
		Version:      p.id.Version,
		Address:      p.id.Address,
		PlatformPort: uint32(p.id.PlatformPort),
		StartedAt:    timestamppb.New(p.started),
		Phase:        backplanev1.InstancePhase(p.phase.Load()),
		SdkVersion:   p.manifest.GetSdkVersion(),
		Commit:       p.id.Commit,
		Transports:   []*backplanev1.TransportStatus{{Name: "consul", Connected: true}},
	}

	if p.config != nil {
		eff := p.config.Effective()
		state.Config, state.Sources = eff.Values, eff.Sources
		state.ConfigRevision = eff.Revision

		if eff.Err != nil {
			state.ConfigError = eff.Err.Error()
			state.ConfigRejectedRevision = eff.RejectedRevision
		}
	}

	if p.nodes != nil {
		state.Nodes = p.nodes()
	}

	if p.transports != nil {
		for _, t := range p.transports() {
			if t.GetName() != "consul" {
				state.Transports = append(state.Transports, t)
			}
		}
	}

	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("instance state: marshal: %w", err)
	}

	return raw, nil
}

// writeState publishes the instance state under the current session. first
// allows evicting another incarnation of this instance (see run).
func (p *Presence) writeState(ctx context.Context, first bool) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()

	raw, err := p.state()
	if err != nil {
		return err
	}

	key := p.key("instances/" + p.id.Instance)

	ok, err := p.acquire(ctx, key, raw)
	if err == nil && !ok {
		if err = p.evict(ctx, key, first); err == nil {
			ok, err = p.acquire(ctx, key, raw)
		}
	}

	switch {
	case err != nil:
		return fmt.Errorf("instance state: %w", err)
	case !ok:
		return fmt.Errorf("%w: %s", ErrStateHeld, key)
	}

	p.mu.Lock()
	p.written = raw
	p.mu.Unlock()

	return nil
}

func (p *Presence) acquire(ctx context.Context, key string, value []byte) (bool, error) {
	ok, _, err := p.client.KV().Acquire(&api.KVPair{Key: key, Value: value, Session: p.currentSession()}, write(ctx))
	if err != nil {
		return false, fmt.Errorf("acquire %s: %w", key, err)
	}

	return ok, nil
}

// evict destroys the session holding key when it is a stale one of this
// process, or — on the first establish only — of an earlier incarnation of
// this instance. Another live incarnation is ErrDuplicateInstance; any other
// holder is left alone (the acquire retry reports ErrStateHeld).
func (p *Presence) evict(ctx context.Context, key string, first bool) error {
	holder, err := p.holder(ctx, key)
	if err != nil || holder == nil || !p.sameInstance(holder.Name) {
		return err
	}

	if holder.Name != p.name && !first {
		return fmt.Errorf("%w: %s held by session %s (%s), this is %s",
			ErrDuplicateInstance, key, holder.ID, holder.Name, p.name)
	}

	p.log.Info("evicting stale session of this instance", xlog.String("session", holder.ID),
		xlog.String("name", holder.Name))

	if _, err := p.client.Session().Destroy(holder.ID, write(ctx)); err != nil {
		return fmt.Errorf("destroy session %s: %w", holder.ID, err)
	}

	return nil
}

// holder is the session locking key, nil when none does.
func (p *Presence) holder(ctx context.Context, key string) (*api.SessionEntry, error) {
	kv, _, err := p.client.KV().Get(key, query(ctx))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", key, err)
	}

	if kv == nil || kv.Session == "" {
		return nil, nil //nolint:nilnil // an unlocked key has no holder
	}

	holder, _, err := p.client.Session().Info(kv.Session, query(ctx))
	if err != nil {
		return nil, fmt.Errorf("session %s: %w", kv.Session, err)
	}

	return holder, nil
}

// sameInstance reports whether a session name is an incarnation of this
// instance id.
func (p *Presence) sameInstance(name string) bool {
	inc, ok := strings.CutPrefix(name, p.id.Instance+incarnationSep)

	return ok && inc != "" && !strings.Contains(inc, incarnationSep)
}

func write(ctx context.Context) *api.WriteOptions { return (&api.WriteOptions{}).WithContext(ctx) }
func query(ctx context.Context) *api.QueryOptions { return (&api.QueryOptions{}).WithContext(ctx) }
