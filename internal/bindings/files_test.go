package bindings_test

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/bindings"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
)

// eventually polls cond until it holds or 10s pass.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}

		time.Sleep(20 * time.Millisecond)
	}
}

// scratchDSN is a fresh database next to BACKPLANE_TEST_PG's, dropped
// after the test.
func scratchDSN(t *testing.T) string {
	t.Helper()

	dsn := os.Getenv("BACKPLANE_TEST_PG")
	if dsn == "" {
		t.Skip("BACKPLANE_TEST_PG not set")
	}

	admin, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}

	name := "backplane_wiring_test_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if _, err := admin.Exec(t.Context(), "CREATE DATABASE "+name); err != nil {
		_ = admin.Close(t.Context())
		t.Skipf("cannot create a scratch database: %v", err)
	}

	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		_ = admin.Close(ctx)
	})

	scratch, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}

	scratch.Path = "/" + name

	return scratch.String()
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestWiringFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Its own database: wiring files own every managed binding of one, so
	// another platform on the shared one (make dev) would delete these.
	m, iam, _ := liveHubAt(t, scratchDSN(t), func(root deps.Scope, m *bindings.Manager) {
		bindings.NewFiles(root, m, dir, 20*time.Millisecond)
	})
	ctx := t.Context()
	hook, rule := iam+".SendEmail", "welcome-"+iam
	ruleID := bindings.FileRuleID(rule)

	binding := func(description string) string {
		return `
bindings:
  - hook: ` + hook + `
    description: ` + description + `
    steps:
      send: {activity: smtp.Send, input: {to: req.to, subject: "'s'", text: req.template}}
    result: {message_id: send.id}
`
	}
	rules := `
---
rules:
  - name: ` + rule + `
    event: ` + iam + `.UserRegistered
    steps:
      send: {activity: smtp.Send, input: {to: event.email, subject: "'hi'", text: event.name}}
  # Waits for a service nobody registered; the rest applies.
  - name: ghost-` + iam + `
    event: ghost.Happened
    steps:
      send: {activity: smtp.Send, input: {to: "'a@b'", subject: "'s'", text: "'t'"}}
`
	writeFile(t, filepath.Join(dir, "mail.yaml"), binding("first")+rules)
	// A ConfigMap's own files are skipped.
	writeFile(t, filepath.Join(dir, "..data", "broken.yaml"), "{")

	current := func() bindings.BindingVersion {
		v, _, err := m.Current(ctx, hook)
		if err != nil {
			t.Fatal(err)
		}

		return v
	}

	eventually(t, "the binding from the file", func() bool { return current().Version == 1 })

	v := current()
	if v.ManagedBy != "mail.yaml" || v.Author != bindings.FilesAuthor || v.Binding.Description != "first" {
		t.Fatalf("v1: %+v", v)
	}

	eventually(t, "the rule from the file", func() bool { _, err := m.Rule(ctx, ruleID); return err == nil })

	if _, err := m.Rule(ctx, bindings.FileRuleID("ghost-"+iam)); err == nil {
		t.Fatal("a rule of an unknown event was saved")
	}

	// The console reads, never writes, what a file defines.
	api, ruleAPI := m.BindingAPI(), m.RuleAPI()

	_, err := api.SaveBinding(ctx, &consolev1.SaveBindingRequest{Definition: v.Binding.PB()})
	if code(err) != codes.FailedPrecondition {
		t.Fatalf("console save: %v", err)
	}

	if _, err := api.DeleteBinding(ctx, &consolev1.DeleteBindingRequest{Hook: hook}); code(err) != codes.FailedPrecondition {
		t.Fatalf("console delete: %v", err)
	}

	if _, err := ruleAPI.DeleteRule(ctx, &consolev1.DeleteRuleRequest{Id: ruleID.String()}); code(err) != codes.FailedPrecondition {
		t.Fatalf("console rule delete: %v", err)
	}

	if _, err := ruleAPI.PauseRule(ctx, &consolev1.PauseRuleRequest{Id: ruleID.String()}); err != nil {
		t.Fatalf("pause stays the console's: %v", err)
	}

	got, err := api.GetBinding(ctx, &consolev1.GetBindingRequest{Hook: hook})
	if err != nil || got.GetVersion().GetManagedBy() != "mail.yaml" {
		t.Fatalf("get: %v %v", got, err)
	}

	// Unchanged files write nothing.
	time.Sleep(200 * time.Millisecond)

	if again := current(); again.Version != 1 {
		t.Fatalf("an unchanged file wrote v%d", again.Version)
	}

	// A changed file is the next version.
	writeFile(t, filepath.Join(dir, "mail.yaml"), binding("second")+rules)
	eventually(t, "the changed binding", func() bool { return current().Version == 2 })

	if v2 := current(); v2.Binding.Description != "second" || v2.Comment != "from mail.yaml" {
		t.Fatalf("v2: %+v", v2)
	}

	// A directory that does not read whole changes nothing.
	writeFile(t, filepath.Join(dir, "zz.yaml"), "bindings: [{hook: "+hook+"}]")
	time.Sleep(200 * time.Millisecond)

	if kept := current(); kept.Version != 2 || kept.Deleted {
		t.Fatalf("a duplicate definition changed the binding: %+v", kept)
	}

	// Moved to another file: the new file manages it.
	if err := os.Remove(filepath.Join(dir, "zz.yaml")); err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(dir, "mail.yaml"), rules)
	writeFile(t, filepath.Join(dir, "iam", "hooks.yml"), binding("second"))
	eventually(t, "the moved binding", func() bool { return current().ManagedBy == filepath.Join("iam", "hooks.yml") })

	// Dropped from every file: a tombstone, and the console's again.
	if err := os.Remove(filepath.Join(dir, "iam", "hooks.yml")); err != nil {
		t.Fatal(err)
	}

	eventually(t, "the tombstone", func() bool { return current().Deleted })

	if gone := current(); gone.ManagedBy != "" || gone.Comment != "removed from "+filepath.Join("iam", "hooks.yml") {
		t.Fatalf("tombstone: %+v", gone)
	}

	if _, err := api.SaveBinding(ctx, &consolev1.SaveBindingRequest{Definition: v.Binding.PB()}); err != nil {
		t.Fatalf("console save after the file let go: %v", err)
	}

	if err := os.Remove(filepath.Join(dir, "mail.yaml")); err != nil {
		t.Fatal(err)
	}

	eventually(t, "the rule's tombstone", func() bool {
		e, err := m.Rule(ctx, ruleID)

		return err == nil && e.Current.Deleted
	})
}
