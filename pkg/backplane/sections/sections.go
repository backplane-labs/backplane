// Package sections is discovery the product defines: a service offers a
// section — a proto message of the product's under a name — and any
// service follows that name across the installation, without a registry
// of its own.
//
//	sections.Offer(root, "komeet.module", &modulev1.Manifest{Name: "media", ...})
//
//	modules := sections.Follow[*modulev1.Manifest](root, "komeet.module")
//	for service, m := range modules.Get() { ... }
//
// The section goes into the service's manifest. Backplane publishes, by
// name, the sections of every serving service (its highest serving
// version) to Consul KV backplane/sections/<name>; Follow watches that
// key while the service runs. Without Consul nothing is known.
package sections

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"sync"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/decl"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
)

// sectionName is lower case, segments joined by '.', '_' or '-'.
var sectionName = regexp.MustCompile(`^[a-z][a-z0-9]*([._-][a-z0-9]+)*$`)

func checkName(name string) {
	if !sectionName.MatchString(name) {
		panic(fmt.Sprintf("sections: name %q must be lower case, segments joined by . _ or - (komeet.module)", name))
	}
}

// Offer declares the service's section name with msg. A name is offered
// once per service; a msg that does not marshal is a declaration error
// (Run fails, Service.Validate reports it).
func Offer(scope deps.Scope, name string, msg proto.Message) {
	checkName(name)

	e, _ := decl.Env(scope, "section "+name)

	value, err := anypb.New(msg)
	if err != nil {
		e.Manifest.Fail(fmt.Errorf("section %s: %w", name, err))

		return
	}

	e.Manifest.Section(name, value)
}

// Live is a section the service offers whose value changes while it runs,
// e.g. with its live configuration. It is immutable and safe for
// concurrent use; the zero Live sets nothing.
type Live[T proto.Message] struct {
	e    *env.Env
	name string
}

// OfferLive declares the service's section name as T, with a value the
// service sets while it runs (Live.Set). The manifest, shared by every
// instance of a version, holds T's empty message; each instance publishes
// its value with its state, and followers see the value of the service's
// highest serving version, from one of its instances. Until an instance
// sets it, followers see the empty message.
func OfferLive[T proto.Message](scope deps.Scope, name string) Live[T] {
	var zero T

	Offer(scope, name, zero.ProtoReflect().New().Interface())

	e, _ := decl.Env(scope, "section "+name)

	return Live[T]{e: e, name: name}
}

// Set publishes msg as the section's value; setting an equal value again
// changes nothing.
func (l Live[T]) Set(msg T) error {
	if l.e == nil {
		return nil
	}

	// Deterministic: an equal value encodes equally and changes nothing.
	value := &anypb.Any{}
	if err := anypb.MarshalFrom(value, msg, proto.MarshalOptions{Deterministic: true}); err != nil {
		return fmt.Errorf("section %s: %w", l.name, err)
	}

	l.e.SetSection(l.name, value)

	return nil
}

// Ref follows one section name: the section of every serving service
// that offers it. Its methods are safe for concurrent use.
type Ref[T proto.Message] struct{ s *state[T] }

type state[T proto.Message] struct {
	mu    sync.RWMutex
	set   map[string]T
	known bool
	subs  map[chan struct{}]struct{}
	empty func() T
}

// Follow follows section name, decoded as T (a pointer to a generated
// message). A service that offers name with another message is left out.
func Follow[T proto.Message](scope deps.Scope, name string) Ref[T] {
	checkName(name)

	e, _ := decl.Env(scope, "section "+name)

	var zero T

	st := &state[T]{
		set: map[string]T{}, subs: map[chan struct{}]struct{}{},
		empty: func() T {
			msg, _ := zero.ProtoReflect().New().Interface().(T)

			return msg
		},
	}

	e.FollowSection(name, st.update)

	return Ref[T]{s: st}
}

// Get is the section of every serving service that offers it, by
// service; empty until known, or when none does.
func (r Ref[T]) Get() map[string]T {
	if r.s == nil {
		return map[string]T{}
	}

	r.s.mu.RLock()
	defer r.s.mu.RUnlock()

	return maps.Clone(r.s.set)
}

// Known reports whether the published set was read: false before the
// first read and without Consul.
func (r Ref[T]) Known() bool {
	if r.s == nil {
		return false
	}

	r.s.mu.RLock()
	defer r.s.mu.RUnlock()

	return r.s.known
}

// Changes signals after every change of the set until ctx ends; one
// pending signal at most.
func (r Ref[T]) Changes(ctx context.Context) <-chan struct{} {
	ch := make(chan struct{}, 1)
	if r.s == nil {
		return ch
	}

	r.s.mu.Lock()
	r.s.subs[ch] = struct{}{}
	r.s.mu.Unlock()

	context.AfterFunc(ctx, func() {
		r.s.mu.Lock()
		delete(r.s.subs, ch)
		r.s.mu.Unlock()
	})

	return ch
}

// update takes a published set; one that does not decode keeps the last.
func (s *state[T]) update(raw []byte) {
	var published backplanev1.SectionSet
	if err := proto.Unmarshal(raw, &published); err != nil {
		return
	}

	set := make(map[string]T, len(published.GetServices()))

	for service, value := range published.GetServices() {
		msg := s.empty()
		if err := value.UnmarshalTo(msg); err == nil {
			set[service] = msg
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	changed := !s.known || !equal(s.set, set)
	s.set, s.known = set, true

	if !changed {
		return
	}

	for ch := range s.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func equal[T proto.Message](a, b map[string]T) bool {
	if len(a) != len(b) {
		return false
	}

	for k, v := range a {
		w, ok := b[k]
		if !ok || !proto.Equal(v, w) {
			return false
		}
	}

	return true
}
