package backplane

import (
	"context"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/node"
)

// presenceNode publishes the manifest and the instance state (phase
// starting) ahead of the connections and the author's tree, so an instance
// waiting for its dependencies is visible; it removes the state last. The
// catalog registration is the register node's, at the serving stage.
func (c *core) presenceNode() {
	presence := c.svc.Child("consul", node.System, false)
	presence.OnStart(func(ctx context.Context) error {
		if c.consulPresence == nil {
			return nil
		}

		return c.consulPresence.Start(ctx, presence)
	})
	presence.OnStop(func(ctx context.Context) error {
		if c.consulPresence == nil {
			return nil
		}

		return c.consulPresence.Stop(ctx)
	})
}

// nodeKind is the kind the instance state reports for an author's node;
// false for the SDK's own.
func nodeKind(k node.Kind) (backplanev1.NodeKind, bool) {
	switch k {
	case node.Component:
		return backplanev1.NodeKind_NODE_KIND_COMPONENT, true
	case node.Dependency:
		return backplanev1.NodeKind_NODE_KIND_DEPENDENCY, true
	case node.Singleton:
		return backplanev1.NodeKind_NODE_KIND_SINGLETON, true
	default:
		return backplanev1.NodeKind_NODE_KIND_UNSPECIFIED, false
	}
}

// nodes reports the author's tree for the instance state, depth-first in
// creation order: every component, dependency and singleton, with the
// readiness of the dependencies.
func (c *core) nodes() []*backplanev1.NodeStatus {
	var out []*backplanev1.NodeStatus

	c.app.Walk(func(n *node.Node) {
		kind, ok := nodeKind(n.Kind())
		if !ok {
			return
		}

		st := &backplanev1.NodeStatus{Path: n.Path(), Kind: kind, Optional: n.Optional()}

		if cond, known := n.Condition(); known && n.Kind() == node.Dependency {
			st.Ready = cond.Ready
			if cond.Err != nil {
				st.Error = cond.Err.Error()
			}
		}

		out = append(out, st)
	})

	return out
}

// transports reports the NATS and Temporal connections and OTLP export
// for the instance state; Consul is added by the presence.
func (c *core) transports() []*backplanev1.TransportStatus {
	var out []*backplanev1.TransportStatus

	if configured, up, err := c.telemetry.Status(); configured {
		st := &backplanev1.TransportStatus{Name: "otlp", Connected: up}
		if err != nil {
			st.Error = err.Error()
		}

		out = append(out, st)
	}

	if c.broker != nil {
		out = append(out, &backplanev1.TransportStatus{Name: "nats", Connected: c.broker.Connected()})
	}

	if c.temporal != nil {
		out = append(out, &backplanev1.TransportStatus{Name: "temporal", Connected: c.temporal.Connected()})
	}

	return out
}
