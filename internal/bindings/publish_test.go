package bindings_test

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/bindings"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
)

// A saved binding is published under its service; deleting the last one
// removes the key.
func TestPublishBound(t *testing.T) {
	t.Parallel()

	addr := os.Getenv("BACKPLANE_TEST_CONSUL")
	if addr == "" {
		t.Skip("BACKPLANE_TEST_CONSUL not set")
	}

	client, err := api.NewClient(&api.Config{Address: addr})
	if err != nil {
		t.Fatal(err)
	}

	m, iam, _ := liveHub(t, func(root deps.Scope, m *bindings.Manager) { bindings.NewPublisher(root, m, client) })
	ctx := t.Context()
	hook := iam + ".SendEmail"
	key := bindings.BoundPrefix + iam

	t.Cleanup(func() { _, _ = client.KV().Delete(key, nil) })

	read := func() ([]string, bool) {
		pair, _, err := client.KV().Get(key, nil)
		if err != nil || pair == nil {
			return nil, false
		}

		var hooks []string
		if json.Unmarshal(pair.Value, &hooks) != nil {
			return nil, false
		}

		return hooks, true
	}

	eventually := func(what string, cond func() bool) {
		t.Helper()

		deadline := time.Now().Add(10 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("not reached: %s", what)
			}

			time.Sleep(50 * time.Millisecond)
		}
	}

	if _, ok := read(); ok {
		t.Fatal("published before any binding")
	}

	def := mustBinding(t, `
hook: `+hook+`
steps:
  send: {activity: smtp.Send, input: {to: req.to, subject: "'s'", text: req.template}}
result: {message_id: send.id}
`).PB()

	if _, err := m.BindingAPI().SaveBinding(ctx, &consolev1.SaveBindingRequest{Definition: def}); err != nil {
		t.Fatal(err)
	}

	eventually("published", func() bool { hooks, ok := read(); return ok && slices.Equal(hooks, []string{"SendEmail"}) })

	if _, err := m.BindingAPI().DeleteBinding(ctx, &consolev1.DeleteBindingRequest{Hook: hook}); err != nil {
		t.Fatal(err)
	}

	eventually("removed", func() bool { _, ok := read(); return !ok })
}
