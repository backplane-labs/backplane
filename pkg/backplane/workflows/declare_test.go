package workflows_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/workflow"

	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	inftemporal "github.com/backplane-labs/backplane/pkg/backplane/infra/temporal"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/link"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/manifest"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/node"
	internal "github.com/backplane-labs/backplane/pkg/backplane/internal/temporal"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/temporal/temporaltest"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/testlog"
	"github.com/backplane-labs/backplane/pkg/backplane/workflows"
)

type (
	order struct {
		Items int `json:"items"`
	}
	shipment struct {
		Parcels int `json:"parcels"`
	}
)

var errNothing = errors.New("nothing to ship")

func ship(_ workflow.Context, in order) (shipment, error) {
	if in.Items == 0 {
		return shipment{}, errNothing
	}

	return shipment{Parcels: (in.Items + 1) / 2}, nil
}

func TestDeclareRecordsManifest(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t)
	workflows.Declare(h.Root(), "Ship", ship, workflows.Describe("ships an order"))

	got := h.Manifest().GetWorkflows()
	if len(got) != 1 || got[0].GetName() != "Ship" || got[0].GetDescription() != "ships an order" ||
		got[0].GetInput() == nil || got[0].GetOutput() == nil {
		t.Fatalf("workflows: %v", got)
	}

	workflows.Declare(h.Root(), "Ship", ship)

	if err := workflows.ManifestError(h.Root()); !errors.Is(err, manifest.ErrDuplicate) {
		t.Fatalf("duplicate: %v", err)
	}
}

func TestDeclarePanics(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		want    string
		declare func(scope deps.Scope)
	}{
		{`workflow name "ship" is not CamelCase`, func(scope deps.Scope) { workflows.Declare(scope, "ship", ship) }},
		{"workflow Ship declared after Run", func(scope deps.Scope) {
			workflows.Seal(scope)
			workflows.Declare(scope, "Ship", ship)
		}},
	} {
		func() {
			defer func() {
				if r, _ := recover().(string); !strings.Contains(r, tc.want) {
					t.Errorf("panic %q, want %q", r, tc.want)
				}
			}()

			tc.declare(backplanetest.New(t).Root())
		}()
	}
}

// A declared workflow is registered on the service's worker and starts by
// its name with a JSON input, as the console starts it.
func TestDeclaredStartable(t *testing.T) {
	t.Parallel()

	svc := temporaltest.Name("decl")
	e := env.New(svc, manifest.New(svc, "0.0.0"))
	root := link.Scope(node.New(svc, testlog.Discard(), e).Child(svc, node.Root, false)).(deps.Component)
	workflows.Declare(root, "Ship", ship)

	c := internal.New(internal.Params{
		Conn: inftemporal.Config{Addr: temporaltest.Addr(t)}, Service: svc, Instance: svc + "-1", Log: testlog.Discard(), Env: e,
	})
	g := temporaltest.NewGroup(t)

	if err := c.Connect(t.Context(), g); err != nil {
		t.Fatal(err)
	}

	if err := c.StartWorker(t.Context(), g); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = c.StopWorker(context.Background())
		_ = c.Close(context.Background())
	})

	tc := temporaltest.Dial(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	run, err := tc.ExecuteWorkflow(ctx, client.StartWorkflowOptions{TaskQueue: workflows.Queue(root)}, "Ship",
		map[string]any{"items": 5})
	if err != nil {
		t.Fatal(err)
	}

	var out shipment
	if err := run.Get(ctx, &out); err != nil || out.Parcels != 3 {
		t.Fatalf("ship: %+v %v", out, err)
	}
}
