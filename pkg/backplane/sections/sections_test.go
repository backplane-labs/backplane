package sections_test

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/decl"
	"github.com/backplane-labs/backplane/pkg/backplane/sections"
)

// An offered section is in the manifest under its name.
func TestOffer(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t, backplanetest.Name("media"))
	sections.Offer(h.Root(), "komeet.module", wrapperspb.String("media"))

	got := h.Manifest().GetSections()["komeet.module"]

	var value wrapperspb.StringValue
	if got == nil || got.UnmarshalTo(&value) != nil || value.GetValue() != "media" {
		t.Fatalf("section %v", got)
	}

	for _, name := range []string{"Komeet", "a..b", "", "1x"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("name %q accepted", name)
				}
			}()

			sections.Offer(h.Root(), name, wrapperspb.String("x"))
		}()
	}
}

// A live section is declared with T's empty message; Set replaces the
// offered value, and setting an equal one again changes nothing.
func TestOfferLive(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t, backplanetest.Name("kratos"))
	login := sections.OfferLive[*wrapperspb.StringValue](h.Root(), "kratos.login")

	var declared wrapperspb.StringValue
	if got := h.Manifest().GetSections()["kratos.login"]; got == nil || got.UnmarshalTo(&declared) != nil || declared.GetValue() != "" {
		t.Fatalf("declared %v", got)
	}

	for _, v := range []string{"password", "password", "code"} {
		if err := login.Set(wrapperspb.String(v)); err != nil {
			t.Fatal(err)
		}

		var value wrapperspb.StringValue
		if got := backplanetest.Offered(h, "kratos.login"); got == nil || got.UnmarshalTo(&value) != nil || value.GetValue() != v {
			t.Fatalf("offered %v, want %q", got, v)
		}
	}

	if err := (sections.Live[*wrapperspb.StringValue]{}).Set(wrapperspb.String("x")); err != nil {
		t.Fatal(err)
	}
}

// A followed name is unknown until published; then the services that
// offer it as T, by service; a service offering another message is left
// out; a change is signaled, a repeat is not.
func TestFollow(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t, backplanetest.Name("platform"))
	modules := sections.Follow[*wrapperspb.StringValue](h.Root(), "komeet.module")

	if modules.Known() || len(modules.Get()) != 0 {
		t.Fatal("known before a publication")
	}

	changes := modules.Changes(t.Context())

	backplanetest.SetSections(h, "komeet.module", map[string]proto.Message{"media": wrapperspb.String("m"), "odd": durationpb.New(time.Second)})

	got := modules.Get()
	if !modules.Known() || len(got) != 1 || got["media"].GetValue() != "m" {
		t.Fatalf("set %v", got)
	}

	select {
	case <-changes:
	default:
		t.Fatal("no change signaled")
	}

	backplanetest.SetSections(h, "komeet.module", map[string]proto.Message{"media": wrapperspb.String("m")})

	select {
	case <-changes:
		t.Fatal("a repeat signaled")
	default:
	}

	backplanetest.SetSections(h, "komeet.module", nil)

	if !modules.Known() || len(modules.Get()) != 0 {
		t.Fatalf("after removal %v", modules.Get())
	}

	select {
	case <-changes:
	default:
		t.Fatal("removal not signaled")
	}
}

// A set published empty is known.
func TestFollowKnownEmpty(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t)
	modules := sections.Follow[*wrapperspb.StringValue](h.Root(), "komeet.module")
	changes := modules.Changes(t.Context())

	backplanetest.SetSections(h, "komeet.module", map[string]proto.Message{})

	if !modules.Known() || len(modules.Get()) != 0 {
		t.Fatalf("known %v, set %v", modules.Known(), modules.Get())
	}

	select {
	case <-changes:
	default:
		t.Fatal("first set not signaled")
	}
}

// Follow is a declaration: after Run it panics instead of following
// nothing.
func TestFollowAfterRun(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t)
	e, _ := decl.Env(h.Root(), "test")
	e.Manifest.Seal() // as Run does before the sections are watched

	defer func() {
		if recover() == nil {
			t.Fatal("Follow after Run did not panic")
		}
	}()

	sections.Follow[*wrapperspb.StringValue](h.Root(), "komeet.module")
}
