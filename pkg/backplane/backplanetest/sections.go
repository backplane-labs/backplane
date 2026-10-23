package backplanetest

import (
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
)

// SetSections publishes section name as Backplane does: services, by
// service, become the set every sections.Follow of name in h's tree reads.
// An empty or nil services is a known empty set; a repeat of the same set
// signals no change.
func SetSections(h *Harness, name string, services map[string]proto.Message) {
	h.t.Helper()

	set := &backplanev1.SectionSet{Services: make(map[string]*anypb.Any, len(services))}

	for service, msg := range services {
		value, err := anypb.New(msg)
		if err != nil {
			h.t.Fatalf("backplanetest: section %s of %s: %v", name, service, err)
		}

		set.Services[service] = value
	}

	raw, err := proto.Marshal(set)
	if err != nil {
		h.t.Fatalf("backplanetest: section %s: %v", name, err)
	}

	for _, fn := range h.env.Followed()[name] {
		fn(raw)
	}
}
