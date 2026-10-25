// Package temporal installs the optional temporal transport.
package temporal

import (
	"github.com/backplane-labs/backplane/pkg/backplane"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	inftemporal "github.com/backplane-labs/backplane/pkg/backplane/infra/temporal"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/decl"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/temporal"
)

// Driver installs the transport; configuration decides whether it is enabled.
func Driver() backplane.Option {
	return backplane.WithWorkflowTransport(func(
		cfg config.Backplane, scope deps.Scope, id backplane.Identity,
	) backplane.WorkflowTransport {
		e, _ := decl.Env(scope, "temporal driver")
		transport := temporal.New(temporal.Params{
			Conn: inftemporal.Config{
				Addr: cfg.Temporal.Addr, Namespace: cfg.Temporal.Namespace, TLS: cfg.Temporal.TLS,
				APIKey: cfg.Temporal.APIKey, DialTimeout: cfg.Temporal.DialTimeout, Encryption: cfg.Temporal.Encryption,
				RequireEncryption: cfg.Temporal.RequireEncryption,
			},
			Service: id.Service, Instance: id.Instance, Log: scope.Log(), Env: e,
			Worker: tuning(cfg.Temporal.Worker), HookTimeout: cfg.Temporal.HookTimeout,
		})
		e.SetWorkflowClient(func() (any, error) { return transport.SDK() })

		// Options failed already, and Run reports it, when this does.
		if codec, err := inftemporal.NewCodec(cfg.Temporal.Encryption); err == nil && cfg.Temporal.Encryption.Enabled() {
			e.SetPayloadCodec(codec)
		}

		return transport
	})
}

// tuning of the Temporal worker from its configuration block.
func tuning(w config.Worker) temporal.Tuning {
	return temporal.Tuning{
		MaxConcurrentActivities:    int(w.MaxConcurrentActivities),
		MaxConcurrentWorkflowTasks: int(w.MaxConcurrentWorkflowTasks),
		ActivityPollers:            int(w.ActivityPollers),
		WorkflowPollers:            int(w.WorkflowPollers),
	}
}
