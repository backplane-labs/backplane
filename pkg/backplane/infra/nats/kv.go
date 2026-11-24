package nats

import (
	"context"
	"errors"
	"fmt"

	"github.com/nats-io/nats.go/jetstream"
)

// MintOnce is the value of key in kv, minted by mint and stored if absent:
// replicas racing on a fresh bucket all end with the one value the first
// stored, never each with its own.
func MintOnce(ctx context.Context, kv jetstream.KeyValue, key string, mint func() ([]byte, error)) ([]byte, error) {
	if entry, err := kv.Get(ctx, key); err == nil {
		return entry.Value(), nil
	} else if !errors.Is(err, jetstream.ErrKeyNotFound) {
		return nil, fmt.Errorf("nats: kv %s: %w", key, err)
	}

	value, err := mint()
	if err != nil {
		return nil, fmt.Errorf("nats: kv %s: mint: %w", key, err)
	}

	if _, err := kv.Create(ctx, key, value); err == nil {
		return value, nil
	} else if !errors.Is(err, jetstream.ErrKeyExists) {
		return nil, fmt.Errorf("nats: kv %s: create: %w", key, err)
	}

	entry, err := kv.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("nats: kv %s: %w", key, err)
	}

	return entry.Value(), nil
}
