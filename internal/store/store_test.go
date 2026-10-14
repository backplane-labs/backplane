package store_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/backplane-labs/backplane/internal/store"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/infra/postgres"
)

var errRollback = errors.New("rollback")

// open starts a store against BACKPLANE_TEST_PG (a database of
// platform-in-a-box, e.g.
// postgres://backplane:backplane@localhost:5433/backplane); skipped without.
func open(t *testing.T) *store.Store {
	t.Helper()

	dsn := os.Getenv("BACKPLANE_TEST_PG")
	if dsn == "" {
		t.Skip("BACKPLANE_TEST_PG not set")
	}

	h := backplanetest.New(t, backplanetest.Name("backplane"))
	dep := deps.NewDependency(h.Root(), store.New(postgres.Config{DSN: config.Secret(dsn)}))
	h.Start()

	if err := backplanetest.Ready(h); err != nil {
		t.Fatal(err)
	}

	return dep.Get()
}

func TestStore(t *testing.T) {
	t.Parallel()

	first := open(t)
	// A second start (another replica, a restart) finds everything applied.
	second := open(t)

	if first.Installation != second.Installation || first.Installation.String() == "" {
		t.Fatalf("installation %v vs %v", first.Installation, second.Installation)
	}

	var inTx string

	err := first.InTx(t.Context(), func(ctx context.Context) error {
		if err := first.DB.QueryRow(ctx, "SELECT current_setting('search_path')").Scan(&inTx); err != nil {
			return err
		}

		if _, err := first.DB.Exec(ctx, "CREATE TEMP TABLE store_test_rollback (x int)"); err != nil {
			return err
		}

		return errRollback
	})
	if !errors.Is(err, errRollback) || inTx != store.Schema {
		t.Fatalf("tx: %v, search_path %q", err, inTx)
	}

	var applied int
	if err := first.Pool.QueryRow(t.Context(), "SELECT count(*) FROM backplane.sqld_migrations").Scan(&applied); err != nil || applied == 0 {
		t.Fatalf("migrations table in the schema: %d %v", applied, err)
	}
}

func TestNoDSN(t *testing.T) {
	t.Parallel()

	_, err := store.New(postgres.Config{}).Provide(t.Context(), deps.Component{})
	if !errors.Is(err, postgres.ErrNoDSN) {
		t.Fatalf("want ErrNoDSN, got %v", err)
	}
}
