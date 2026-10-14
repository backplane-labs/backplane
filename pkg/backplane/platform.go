package backplane

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/pprof"
	"runtime"

	"github.com/gopherex/xlog"
	"github.com/gopherex/xprobe/pkg/probe"

	"github.com/backplane-labs/backplane/pkg/backplane/build"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/health"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/manifest"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/recovery"
)

const (
	infoPath  = "/_backplane/info"
	pprofPath = "/debug/pprof/"
)

// Transport readiness errors.
var (
	errNATSDown     = errors.New("nats not connected")
	errTemporalDown = errors.New("temporal not connected")
)

// platformHandler: probes open, everything else behind the guard; a
// panicking handler answers 500.
func (c *core) platformHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle(probesPath, c.health.HTTP())
	mux.Handle("/", c.guard.HTTP(c.platform))

	return recovery.HTTP(c.log, recovery.HTTPPlatform, mux)
}

// platformRoutes adds the SDK's own guarded endpoints: build info and,
// when enabled, pprof.
func (c *core) platformRoutes() {
	c.platform.HandleFunc("GET "+infoPath, c.info)

	if c.cfg.Pprof {
		c.platform.HandleFunc(pprofPath, pprof.Index)
		c.platform.HandleFunc(pprofPath+"cmdline", pprof.Cmdline)
		c.platform.HandleFunc(pprofPath+"profile", pprof.Profile)
		c.platform.HandleFunc(pprofPath+"symbol", pprof.Symbol)
		c.platform.HandleFunc(pprofPath+"trace", pprof.Trace)
	}
}

// Info is what /_backplane/info answers.
type Info struct {
	Service     string `json:"service"`
	Version     string `json:"version"`
	Instance    string `json:"instance"`
	Advertise   string `json:"advertise"`
	Environment string `json:"environment,omitempty"`
	SDKVersion  string `json:"sdk_version"`
	GoVersion   string `json:"go_version"`
	Commit      string `json:"commit,omitempty"`
	BuildDate   string `json:"build_date,omitempty"`
}

func (c *core) info(w http.ResponseWriter, _ *http.Request) {
	b := build.Get()
	info := Info{
		Service: c.id.Service, Version: c.id.Version, Instance: c.id.Instance, Advertise: c.id.Advertise,
		Environment: c.id.Environment, SDKVersion: manifest.SDKVersion(), GoVersion: runtime.Version(),
		Commit: b.Commit, BuildDate: b.Date,
	}

	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(info); err != nil {
		c.log.Debug("info: write", xlog.Err(err))
	}
}

// requireTransports makes the connections the author asked for part of
// readiness (RequireNATS, RequireTemporal); Open checked they are
// configured.
func (c *core) requireTransports() {
	if c.opts.requireNATS && c.broker != nil {
		c.health.Add(health.Ready, probe.FromError(func(context.Context) error {
			if !c.broker.Connected() {
				return errNATSDown
			}

			return nil
		}))
	}

	if c.opts.requireTemporal && c.temporal != nil {
		c.health.Add(health.Ready, probe.FromError(func(context.Context) error {
			if !c.temporal.Connected() {
				return errTemporalDown
			}

			return nil
		}))
	}
}
