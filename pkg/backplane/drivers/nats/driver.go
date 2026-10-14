// Package nats installs the optional nats transport.
package nats

import (
	"fmt"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/backplane-labs/backplane/pkg/backplane"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	infranats "github.com/backplane-labs/backplane/pkg/backplane/infra/nats"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/broker"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/decl"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/link"
)

// Driver installs the transport; configuration decides whether it is enabled.
func Driver() backplane.Option {
	return backplane.WithEventTransport(func(
		cfg config.Backplane, scope deps.Scope, id backplane.Identity,
	) backplane.EventTransport {
		e, _ := decl.Env(scope, "nats driver")
		transport := broker.New(broker.Params{
			Conn:           infranats.Config{URL: cfg.NATS.URL, Creds: cfg.NATS.Creds, TLS: cfg.NATS.TLS},
			PublishTimeout: cfg.NATS.PublishTimeout,
			Service:        id.Service, Instance: id.Instance, Version: id.Version, Log: scope.Log(), Env: e,
			Streams: broker.Streams{
				MaxAge: cfg.NATS.MaxAge, MaxBytes: cfg.NATS.MaxBytes, Replicas: int(cfg.NATS.Replicas),
				Duplicates: cfg.NATS.DedupWindow, DeadMaxAge: cfg.NATS.DLQMaxAge,
			},
		})

		return transport
	})
}

// JetStream returns the native client of the installed NATS transport.
func JetStream(scope deps.Scope) (jetstream.JetStream, error) {
	n := link.NodeOf(scope)
	if n != nil {
		if e := env.Of(n); e != nil {
			if b, ok := e.Broker().(interface {
				JetStream() (jetstream.JetStream, error)
			}); ok {
				return b.JetStream() //nolint:wrapcheck // broker wraps ErrUnavailable
			}
		}
	}

	return nil, fmt.Errorf("nats: jetstream: %w", env.ErrUnavailable)
}
