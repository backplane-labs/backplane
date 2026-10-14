package backplane_test

import (
	"bytes"
	"net"
	"strings"
	"testing"

	"github.com/gopherex/xlog"

	"github.com/backplane-labs/backplane/pkg/backplane"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/link"
)

func TestLogLevelLive(t *testing.T) {
	t.Parallel()

	cfg := config.Backplane{LogLevel: config.LiveOf("info")}
	log := backplane.NewLogger(backplane.Identity{Service: "svc"}, nil, cfg)

	if log.Enabled(xlog.DebugLevel) || !log.Enabled(xlog.InfoLevel) {
		t.Fatal("start at info")
	}

	link.SetLive(&cfg.LogLevel, "debug")

	if !log.Enabled(xlog.DebugLevel) {
		t.Fatal("debug not applied")
	}

	link.SetLive(&cfg.LogLevel, "loud") // invalid: kept

	if !log.Enabled(xlog.DebugLevel) {
		t.Fatal("invalid level changed the level")
	}

	link.SetLive(&cfg.LogLevel, "error")

	if log.Enabled(xlog.WarnLevel) || !log.Enabled(xlog.ErrorLevel) {
		t.Fatal("error not applied")
	}
}

func TestGivenLoggerKeepsItsLevel(t *testing.T) {
	t.Parallel()

	cfg := config.Backplane{LogLevel: config.LiveOf("debug")}
	given := xlog.NewJSON(xlog.WithLevel(xlog.WarnLevel), xlog.WithWriter(&bytes.Buffer{}))
	log := backplane.NewLogger(backplane.Identity{Service: "svc"}, given, cfg)

	link.SetLive(&cfg.LogLevel, "trace")

	if log.Enabled(xlog.InfoLevel) {
		t.Fatal("log_level applied to the author's logger")
	}
}

func TestPickAddress(t *testing.T) {
	t.Parallel()

	ips := func(s ...string) []net.IP {
		out := make([]net.IP, len(s))
		for i, a := range s {
			out[i] = net.ParseIP(a)
		}

		return out
	}

	for _, tc := range []struct {
		in   []net.IP
		want string
	}{
		{ips("127.0.0.1", "10.1.2.3"), "10.1.2.3"},
		{ips("::1", "2001:db8::1", "192.168.1.5"), "192.168.1.5"},
		{ips("::1", "fe80::1", "2001:db8::7"), "2001:db8::7"},
		{ips("127.0.0.1", "::1", "fe80::1"), ""},
		{nil, ""},
	} {
		if got := backplane.PickAddress(tc.in); got != tc.want {
			t.Errorf("%v: got %q want %q", tc.in, got, tc.want)
		}
	}
}

func TestLoopbackAdvertiseWarns(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	log := xlog.NewJSON(xlog.WithWriter(&buf))
	cfg := config.Backplane{Consul: config.Consul{Addr: "consul:8500", Register: true}}

	backplane.WarnUnreachable(backplane.Identity{Advertise: "10.0.0.1"}, log, cfg)
	backplane.WarnUnreachable(backplane.Identity{Advertise: "127.0.0.1"}, log, config.Backplane{})

	if buf.Len() != 0 {
		t.Fatalf("unexpected warning: %s", buf.String())
	}

	backplane.WarnUnreachable(backplane.Identity{Advertise: "::1"}, log, cfg)

	if !strings.Contains(buf.String(), "advertise address is loopback") {
		t.Fatalf("no warning: %s", buf.String())
	}
}
