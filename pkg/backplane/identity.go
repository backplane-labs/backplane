package backplane

import (
	"context"
	"net"
	"os"

	"go.opentelemetry.io/otel/log/global"

	"github.com/gopherex/xlog"
	xlogtrace "github.com/gopherex/xtrace/contrib/libs/xlog"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
)

const loopback = "127.0.0.1"

// Identity is how the instance names itself everywhere: logs, telemetry,
// Consul, the manifest.
type Identity struct {
	Service     string
	Version     string
	Instance    string
	Advertise   string
	Environment string
}

// resolve fills the identity from options, then the SDK block, then the host.
func (id Identity) resolve(ctx context.Context, cfg config.Backplane) Identity {
	id.Instance = first(id.Instance, cfg.Instance)
	if id.Instance == "" {
		host, _ := os.Hostname()
		id.Instance = id.Service + "-" + host
	}

	id.Advertise = first(id.Advertise, cfg.Advertise, os.Getenv("POD_IP"))
	if id.Advertise == "" {
		id.Advertise = hostAddress(ctx)
	}

	id.Version = first(id.Version, "0.0.0")
	id.Environment = cfg.Environment

	return id
}

// logger is the given one or JSON to stdout teed into the OpenTelemetry
// logs pipeline (a no-op until telemetry exports logs), with trace fields
// and span observation; every record carries the identity. The default
// logger's level follows the live log_level: a value that does not parse
// keeps the previous level and is reported. A given logger (the Logger
// option) is the author's: its level, outputs and OpenTelemetry tee are
// left as they are and log_level does not apply.
func (id Identity) logger(given *xlog.Logger, cfg config.Backplane) *xlog.Logger {
	log := given
	if log == nil {
		level, err := xlog.ParseLevel(cfg.LogLevel.Get())
		atomic := xlog.NewAtomicLevel(level)

		// Both outputs pass everything; the atomic level gates the tee.
		stdout := xlog.NewJSON(xlog.WithLevel(xlog.TraceLevel)).Core()
		otlp := xlogtrace.Core(global.GetLoggerProvider().Logger(id.Service))
		log = xlog.NewJSON(append(xlogtrace.Options(),
			xlog.WithAtomicLevel(atomic), xlog.WithCore(xlog.NewTeeCore(stdout, otlp)))...)

		if err != nil {
			log.Warn("log level", xlog.Err(err), xlog.String("using", level.String()))
		}

		cfg.LogLevel.Watch(func(v string) { setLevel(log, atomic, v) })
	}

	return log.With(
		xlog.String("service", id.Service), xlog.String("instance", id.Instance), xlog.String("version", id.Version),
	)
}

// setLevel applies a live log_level; one that does not parse keeps the
// current level.
func setLevel(log *xlog.Logger, atomic *xlog.AtomicLevel, v string) {
	level, err := xlog.ParseLevel(v)
	if err != nil {
		log.Warn("log level not changed", xlog.Err(err), xlog.String("keeping", atomic.Level().String()))

		return
	}

	if level != atomic.Level() {
		atomic.Set(level)
		log.Info("log level changed", xlog.String("level", level.String()))
	}
}

// warnUnreachable reports an advertise address other components cannot
// reach while the SDK registers it in Consul.
func (id Identity) warnUnreachable(log *xlog.Logger, cfg config.Backplane) {
	if !cfg.Consul.Enabled() || !cfg.Consul.Register {
		return
	}

	if ip := net.ParseIP(id.Advertise); id.Advertise == "localhost" || (ip != nil && ip.IsLoopback()) {
		log.Warn("advertise address is loopback: the instance registers in consul at an address "+
			"no other host can reach; set BACKPLANE_ADVERTISE or POD_IP",
			xlog.String("advertise", id.Advertise))
	}
}

func first(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}

	return ""
}

// hostAddress is the hostname's address, else an interface's: a global
// unicast IPv4 first, then IPv6; loopback when there is none.
func hostAddress(ctx context.Context) string {
	var candidates []net.IP

	if host, err := os.Hostname(); err == nil {
		if addrs, err := net.DefaultResolver.LookupHost(ctx, host); err == nil {
			for _, a := range addrs {
				candidates = append(candidates, net.ParseIP(a))
			}
		}
	}

	if ip := pickAddress(candidates); ip != "" {
		return ip
	}

	if addrs, err := net.InterfaceAddrs(); err == nil {
		candidates = candidates[:0]

		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				candidates = append(candidates, n.IP)
			}
		}
	}

	if ip := pickAddress(candidates); ip != "" {
		return ip
	}

	return loopback
}

// pickAddress is the first global unicast IPv4 of ips, else the first
// global unicast IPv6; empty when neither.
func pickAddress(ips []net.IP) string {
	var ipv6 string

	for _, addr := range ips {
		switch {
		case addr == nil || !addr.IsGlobalUnicast():
		case addr.To4() != nil:
			return addr.String()
		case ipv6 == "":
			ipv6 = addr.String()
		}
	}

	return ipv6
}
