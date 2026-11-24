// Package manifest collects a service's declarations into a
// backplanev1.Manifest. Declarations are recorded as they happen; the first
// error is kept and returned by Build.
package manifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"runtime/debug"
	"sort"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	sp "github.com/gopherex/schemapb/go/schemapb"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/apifiles"
)

const (
	sdkModule = "github.com/backplane-labs/backplane"
	// MaxSize is Consul's KV value limit; the manifest is one value.
	MaxSize = 512 * 1024
)

// Errors.
var (
	// ErrTooLarge: the manifest does not fit one Consul KV value.
	ErrTooLarge = errors.New("manifest: larger than a Consul KV value (512 KiB)")
	// ErrDuplicate: something declared or registered twice.
	ErrDuplicate = errors.New("manifest: duplicate")
)

// Builder accumulates declarations until Seal; declaring after that is a
// programming error and panics.
type Builder struct {
	mu     sync.Mutex
	m      *backplanev1.Manifest
	errs   []error
	sealed bool
}

// New starts a manifest for service@version.
func New(service, version string) *Builder {
	return &Builder{
		m: &backplanev1.Manifest{Service: service, Version: version, SdkVersion: sdkVersion()},
	}
}

func (b *Builder) with(what string, fn func(m *backplanev1.Manifest) error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.sealed {
		panic("backplane: " + what + " declared after the service started")
	}

	if err := fn(b.m); err != nil {
		b.errs = append(b.errs, err)
	}
}

// Seal ends the declaration phase.
func (b *Builder) Seal() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.sealed = true
}

// Sealed reports whether declarations have ended.
func (b *Builder) Sealed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.sealed
}

// Config records the configuration schema and its live paths.
func (b *Builder) Config(schema *sp.Schema, live []string) {
	b.with("config", func(m *backplanev1.Manifest) error {
		m.Config = section(schema)
		m.Config.Live = live

		return nil
	})
}

// Internal records internal API service names.
func (b *Builder) Internal(services []string) {
	b.with("internal API", func(m *backplanev1.Manifest) error {
		m.InternalServices = append(m.InternalServices, services...)

		return nil
	})
}

// UI records the bundle hash and sdk_major from plugin.json.
func (b *Builder) UI(bundle fs.FS) {
	b.with("ui", func(m *backplanev1.Manifest) error {
		if m.GetUi() != nil {
			return fmt.Errorf("%w: ui declared twice", ErrDuplicate)
		}

		ui, err := uiOf(bundle)
		m.Ui = ui

		return err
	})
}

// Fail records an error found outside the builder.
func (b *Builder) Fail(err error) {
	b.with("declaration", func(*backplanev1.Manifest) error { return err })
}

// Build returns a copy of the manifest, or
// every declaration error.
func (b *Builder) Build() (*backplanev1.Manifest, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.errs) > 0 {
		return nil, errors.Join(b.errs...)
	}

	m := proto.CloneOf(b.m)
	sort.Strings(m.GetInternalServices()) // in place: the clone's own slice

	if size := proto.Size(m); size > MaxSize {
		return nil, fmt.Errorf("%w: %d bytes", ErrTooLarge, size)
	}

	return m, nil
}

// Registry is a gRPC registrar that can list what it holds: *grpc.Server
// and the ws-proto registrar.
type Registry interface {
	grpc.ServiceRegistrar
	GetServiceInfo() map[string]grpc.ServiceInfo
}

// Register runs register against dst and returns the services it added. A
// service already on dst is an error instead of grpc's process exit.
func Register(dst Registry, register func(grpc.ServiceRegistrar)) ([]string, error) {
	r := &checked{dst: dst}
	register(r)

	sort.Strings(r.added)

	return r.added, errors.Join(r.errs...)
}

type checked struct {
	dst   Registry
	added []string
	errs  []error
}

func (c *checked) RegisterService(desc *grpc.ServiceDesc, impl any) {
	if _, dup := c.dst.GetServiceInfo()[desc.ServiceName]; dup {
		c.errs = append(c.errs, fmt.Errorf("%w: gRPC service %s registered twice on one server",
			ErrDuplicate, desc.ServiceName))

		return
	}

	c.dst.RegisterService(desc, impl)
	c.added = append(c.added, desc.ServiceName)
}

func section(schema *sp.Schema) *backplanev1.ConfigSection {
	sec := &backplanev1.ConfigSection{Schema: schema}
	for _, f := range schema.GetFields() {
		sec.Keys = append(sec.Keys, f.GetName())
	}

	return sec
}

// UIHash addresses an immutable snapshot of the bundle's paths and bytes.
func UIHash(bundle fs.FS) (string, error) {
	if files, ok := bundle.(*apifiles.Files); ok {
		return files.Hash(), nil
	}

	files, err := apifiles.Snapshot(bundle, "plugin.json")
	if err != nil {
		return "", fmt.Errorf("manifest: ui bundle: %w", err)
	}

	return files.Hash(), nil
}

func uiOf(bundle fs.FS) (*backplanev1.UI, error) {
	hash, err := UIHash(bundle)
	if err != nil {
		return nil, err
	}

	ui := &backplanev1.UI{Hash: hash}

	raw, err := fs.ReadFile(bundle, "plugin.json")
	if err != nil {
		return nil, fmt.Errorf("manifest: ui bundle: %w", err)
	}

	var plugin struct {
		SDKMajor uint32 `json:"sdk_major"`
	}
	if err := json.Unmarshal(raw, &plugin); err != nil {
		return nil, fmt.Errorf("manifest: plugin.json: %w", err)
	}

	ui.SdkMajor = plugin.SDKMajor

	return ui, nil
}

func sdkVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "(devel)"
	}

	if bi.Main.Path == sdkModule {
		return bi.Main.Version
	}

	for _, d := range bi.Deps {
		if d.Path == sdkModule {
			return d.Version
		}
	}

	return "(devel)"
}
