package bindings

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/hashicorp/consul/api"

	"github.com/gopherex/xlog"

	"github.com/backplane-labs/backplane/pkg/backplane/deps"
)

// BoundPrefix is where each service's bound hooks are published:
// BoundPrefix+<service> holds a JSON array of hook names without the
// service prefix. The SDK watches its own key (pkg/backplane's consul
// driver), so a service can skip work for a hook nobody bound.
const BoundPrefix = "backplane/bindings/"

// publishResync bounds how long a lost write or a foreign edit of the
// prefix stays unrepaired.
const publishResync = 30 * time.Second

// Publisher keeps BoundPrefix equal to the bindings in force: a pass at
// start, after every Changes signal and every publishResync. Replicas run
// it side by side; they all write what PostgreSQL holds.
type Publisher struct {
	deps.Component

	m      *Manager
	consul *api.Client
}

// NewPublisher creates the component under parent.
func NewPublisher(parent deps.Scope, m *Manager, consul *api.Client) *Publisher {
	p := &Publisher{Component: deps.NewComponent(parent, "bindings-publisher"), m: m, consul: consul}
	p.Go(p.loop)

	return p
}

func (p *Publisher) loop(ctx context.Context) error {
	changes := p.m.Changes(ctx)

	tick := time.NewTicker(publishResync)
	defer tick.Stop()

	failing := false

	for {
		err := p.publish(ctx)

		switch {
		case ctx.Err() != nil:
			return nil
		case err != nil && !failing:
			p.Log().Warn("publishing bound hooks failed: retrying", xlog.Err(err))
		case err == nil && failing:
			p.Log().Info("publishing bound hooks recovered")
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

// publish writes every service's bound hooks and deletes the keys of
// services left without any.
func (p *Publisher) publish(ctx context.Context) error {
	current, err := p.m.CurrentAll(ctx)
	if err != nil {
		return err
	}

	bound := map[string][]string{}

	for i := range current {
		if current[i].Deleted {
			continue
		}

		service, hook, ok := strings.Cut(current[i].Hook, ".")
		if !ok {
			continue
		}

		bound[service] = append(bound[service], hook)
	}

	kv := p.consul.KV()

	existing, _, err := kv.List(BoundPrefix, (&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return fmt.Errorf("list %s: %w", BoundPrefix, err)
	}

	held := make(map[string][]byte, len(existing))
	for _, pair := range existing {
		held[strings.TrimPrefix(pair.Key, BoundPrefix)] = pair.Value
	}

	write := (&api.WriteOptions{}).WithContext(ctx)

	for service, hooks := range bound {
		slices.Sort(hooks)

		value, err := json.Marshal(hooks)
		if err != nil {
			return fmt.Errorf("encode %s: %w", service, err)
		}

		if bytes.Equal(held[service], value) {
			continue
		}

		if _, err := kv.Put(&api.KVPair{Key: BoundPrefix + service, Value: value}, write); err != nil {
			return fmt.Errorf("write %s: %w", service, err)
		}
	}

	for service := range held {
		if _, ok := bound[service]; ok {
			continue
		}

		if _, err := kv.Delete(BoundPrefix+service, write); err != nil {
			return fmt.Errorf("delete %s: %w", service, err)
		}
	}

	return nil
}
