package nats_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/backplane-labs/backplane/pkg/backplane/infra/nats"
)

// Replicas racing to mint on a fresh bucket all end with the one stored
// value; a later call reads it without minting.
func TestMintOnce(t *testing.T) {
	t.Parallel()

	srv, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}

	go srv.Start()
	t.Cleanup(srv.Shutdown)

	if !srv.ReadyForConnections(5 * time.Second) {
		t.Fatal("nats server not ready")
	}

	conn, err := natsgo.Connect(srv.ClientURL())
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(conn.Close)

	js, err := jetstream.New(conn)
	if err != nil {
		t.Fatal(err)
	}

	kv, err := js.CreateKeyValue(t.Context(), jetstream.KeyValueConfig{Bucket: "keys"})
	if err != nil {
		t.Fatal(err)
	}

	var (
		minted atomic.Int32
		wg     sync.WaitGroup
		got    = make([][]byte, 8)
	)

	for i := range got {
		wg.Go(func() {
			value, err := nats.MintOnce(t.Context(), kv, "pair", func() ([]byte, error) {
				return []byte{byte('a' + minted.Add(1))}, nil
			})
			if err != nil {
				t.Error(err)
			}

			got[i] = value
		})
	}

	wg.Wait()

	for _, value := range got {
		if string(value) != string(got[0]) {
			t.Fatalf("replicas disagree: %q", got)
		}
	}

	again, err := nats.MintOnce(t.Context(), kv, "pair", func() ([]byte, error) {
		t.Fatal("minted again")

		return nil, nil
	})
	if err != nil || string(again) != string(got[0]) {
		t.Fatalf("again %q %v", again, err)
	}
}
