// Package sections publishes the services' own manifest sections
// (pkg/backplane/sections): by name, the section of every serving
// service's highest serving version, to Consul KV Prefix+<name> as a
// backplanev1.SectionSet. The SDK follows a name by watching its key.
package sections

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/hashicorp/consul/api"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/gopherex/xlog"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/registry"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
)

// Prefix is where the sections are published; the SDK's Consul driver
// mirrors it.
const Prefix = "backplane/sections/"

// resync bounds how long a lost write or a foreign edit of the prefix
// stays unrepaired.
const resync = 30 * time.Second

// Source is the installation's catalog.
type Source interface {
	Current() registry.Catalog
	Changes(ctx context.Context) <-chan struct{}
}

// Publisher keeps Prefix equal to the serving services' sections: a pass
// at start, after every catalog change and every resync. Replicas run it
// side by side; they all write the same.
type Publisher struct {
	deps.Component

	src    Source
	consul *api.Client
}

// NewPublisher creates the component under parent.
func NewPublisher(parent deps.Scope, src Source, consul *api.Client) *Publisher {
	p := &Publisher{Component: deps.NewComponent(parent, "sections-publisher"), src: src, consul: consul}
	p.Go(p.loop)

	return p
}

func (p *Publisher) loop(ctx context.Context) error {
	changes := p.src.Changes(ctx)

	tick := time.NewTicker(resync)
	defer tick.Stop()

	failing := false

	for {
		err := p.publish(ctx)

		switch {
		case ctx.Err() != nil:
			return nil
		case err != nil && !failing:
			p.Log().Warn("publishing sections failed: retrying", xlog.Err(err))
		case err == nil && failing:
			p.Log().Info("publishing sections recovered")
		}

		failing = err != nil

		select {
		case <-ctx.Done():
			return nil
		case <-changes:
		case <-tick.C:
		}
	}
}

// Sets are the sections of cat by name: each serving service's highest
// serving version's, a live value (sections.OfferLive) replacing the
// manifest's.
func Sets(cat registry.Catalog) map[string]*backplanev1.SectionSet {
	sets := map[string]*backplanev1.SectionSet{}

	for name, svc := range cat.Services {
		m := svc.Available()
		live := liveValues(svc, m.GetVersion())

		for section, value := range m.GetSections() {
			if v, ok := live[section]; ok && v.GetTypeUrl() == value.GetTypeUrl() {
				value = v
			}

			set := sets[section]
			if set == nil {
				set = &backplanev1.SectionSet{Services: map[string]*anypb.Any{}}
				sets[section] = set
			}

			set.Services[name] = value
		}
	}

	return sets
}

// liveValues are the live section values of svc's serving instances of
// version. Instances agree but for a change in flight; the one with the
// highest id wins, so the choice does not flap between them.
func liveValues(svc registry.Service, version string) map[string]*anypb.Any {
	out := map[string]*anypb.Any{}
	instances := slices.SortedFunc(slices.Values(svc.Instances), func(a, b registry.Instance) int {
		return strings.Compare(a.ID, b.ID)
	})

	for _, in := range instances {
		if !in.Available() || in.State.GetVersion() != version {
			continue
		}

		maps.Copy(out, in.State.GetSections())
	}

	return out
}

// publish writes every name's set and deletes the keys of names no
// serving service offers.
func (p *Publisher) publish(ctx context.Context) error {
	sets := Sets(p.src.Current())
	kv := p.consul.KV()

	existing, _, err := kv.List(Prefix, (&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return fmt.Errorf("list %s: %w", Prefix, err)
	}

	held := make(map[string][]byte, len(existing))
	for _, pair := range existing {
		held[strings.TrimPrefix(pair.Key, Prefix)] = pair.Value
	}

	write := (&api.WriteOptions{}).WithContext(ctx)

	for name, set := range sets {
		value, err := proto.MarshalOptions{Deterministic: true}.Marshal(set)
		if err != nil {
			return fmt.Errorf("encode %s: %w", name, err)
		}

		if bytes.Equal(held[name], value) {
			continue
		}

		if _, err := kv.Put(&api.KVPair{Key: Prefix + name, Value: value}, write); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}

	for name := range held {
		if _, ok := sets[name]; ok {
			continue
		}

		if _, err := kv.Delete(Prefix+name, write); err != nil {
			return fmt.Errorf("delete %s: %w", name, err)
		}
	}

	return nil
}
