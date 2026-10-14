package config_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/link"
)

func TestSecretMasksEveryVerb(t *testing.T) {
	t.Parallel()

	s := config.Secret("hunter2")

	for _, verb := range []string{"%v", "%s", "%d", "%q", "%x", "%X", "%+v", "%#v", "%10s", "%-5v"} {
		if out := fmt.Sprintf(verb, s); out != "***" {
			t.Errorf("%s: %q", verb, out)
		}
	}

	if s.String() != "***" || s.GoString() != "***" || s.Reveal() != "hunter2" {
		t.Fatalf("String %q GoString %q Reveal %q", s.String(), s.GoString(), s.Reveal())
	}

	type holder struct {
		Token config.Secret `json:"token"`
		Ptr   *config.Secret
	}

	h := holder{Token: s, Ptr: &s}
	for _, verb := range []string{"%v", "%+v", "%#v"} {
		if out := fmt.Sprintf(verb, h); strings.Contains(out, "hunter2") {
			t.Errorf("%s of a struct leaked: %s", verb, out)
		}
	}

	if out := fmt.Sprintf("%v|%s", h.Ptr, h.Ptr); out != "***|***" {
		t.Errorf("pointer: %q", out)
	}
}

func TestSecretEncodings(t *testing.T) {
	t.Parallel()

	s := config.Secret("hunter2")

	text, err := s.MarshalText()
	if err != nil || string(text) != "***" {
		t.Fatalf("MarshalText: %q %v", text, err)
	}

	raw, err := json.Marshal(struct {
		Token config.Secret  `json:"token"`
		Ptr   *config.Secret `json:"ptr"`
	}{Token: s, Ptr: &s})
	if err != nil || string(raw) != `{"token":"***","ptr":"***"}` {
		t.Fatalf("json: %s %v", raw, err)
	}

	var buf bytes.Buffer

	slog.New(slog.NewTextHandler(&buf, nil)).Info("x", "secret", s)
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("x", "secret", s)

	if strings.Contains(buf.String(), "hunter2") {
		t.Fatalf("slog leaked: %s", buf.String())
	}
}

func TestLiveOfAndZero(t *testing.T) {
	t.Parallel()

	l := config.LiveOf(42)
	if l.Get() != 42 {
		t.Fatalf("LiveOf: %d", l.Get())
	}

	copied := l // copies share the value
	link.SetLive(&l, 7)

	if copied.Get() != 7 {
		t.Fatalf("copy did not follow: %d", copied.Get())
	}

	var zero config.Live[string]
	if zero.Get() != "" {
		t.Fatalf("zero: %q", zero.Get())
	}

	zero.Watch(func(string) { t.Error("zero Live must never fire") })

	link.SetLive(&zero, "set")

	if zero.Get() != "set" {
		t.Fatalf("SetLive on zero: %q", zero.Get())
	}
}

func TestLiveJSON(t *testing.T) {
	t.Parallel()

	l := config.LiveOf("x")

	raw, err := json.Marshal(&l)
	if err != nil || string(raw) != `"x"` {
		t.Fatalf("live: %s %v", raw, err)
	}

	type section struct {
		Level config.Live[string] `json:"level"`
		Limit config.Live[int]    `json:"limit"`
	}

	s := &section{Level: config.LiveOf("debug"), Limit: config.LiveOf(3)}

	raw, err = json.Marshal(s)
	if err != nil || string(raw) != `{"level":"debug","limit":3}` {
		t.Fatalf("section: %s %v", raw, err)
	}
}

func TestSetLiveFiresWatchers(t *testing.T) {
	t.Parallel()

	l := config.LiveOf("a")

	var got []string

	l.Watch(func(v string) { got = append(got, "first:"+v) })
	l.Watch(func(v string) { got = append(got, "second:"+v) })

	link.SetLive(&l, "b")
	link.SetLive(&l, "b") // unchanged: no notification
	link.SetLive(&l, "c")
	link.SetLive(&l, 1) // wrong type: ignored

	want := []string{"first:b", "second:b", "first:c", "second:c"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("watchers: %v, want %v", got, want)
	}

	if l.Get() != "c" {
		t.Fatalf("value: %q", l.Get())
	}
}

func TestPanickingWatcherIsContained(t *testing.T) {
	t.Parallel()

	l := config.LiveOf(1)

	var after int

	l.Watch(func(int) { panic("boom") })
	l.Watch(func(v int) { after = v })

	link.SetLive(&l, 2)

	if after != 2 || l.Get() != 2 {
		t.Fatalf("after a panicking watcher: watcher saw %d, value %d", after, l.Get())
	}
}
