package sections_test

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
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

	publish := func(services map[string]proto.Message) {
		t.Helper()

		set := &backplanev1.SectionSet{Services: map[string]*anypb.Any{}}

		for service, msg := range services {
			value, err := anypb.New(msg)
			if err != nil {
				t.Fatal(err)
			}

			set.Services[service] = value
		}

		raw, err := proto.Marshal(set)
		if err != nil {
			t.Fatal(err)
		}

		sections.Publish(h.Root(), "komeet.module", raw)
	}

	publish(map[string]proto.Message{"media": wrapperspb.String("m"), "odd": durationpb.New(time.Second)})

	got := modules.Get()
	if !modules.Known() || len(got) != 1 || got["media"].GetValue() != "m" {
		t.Fatalf("set %v", got)
	}

	select {
	case <-changes:
	default:
		t.Fatal("no change signaled")
	}

	publish(map[string]proto.Message{"media": wrapperspb.String("m")})

	select {
	case <-changes:
		t.Fatal("a repeat signaled")
	default:
	}

	sections.Publish(h.Root(), "komeet.module", nil)

	if len(modules.Get()) != 0 {
		t.Fatalf("after removal %v", modules.Get())
	}
}
