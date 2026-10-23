package ops_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.temporal.io/sdk/client"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/ops"
	"github.com/backplane-labs/backplane/internal/registry"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
)

// A wiring file's schedules are created, kept as the file says while the
// operator's pause stays, refused to console edits, take over a schedule
// made by hand, and go when the file drops them.
func TestScheduleFilesLive(t *testing.T) {
	t.Parallel()

	c := liveTemporal(t)
	svc := unique("schedfiles")
	ctx := t.Context()

	hub := registry.NewHub()
	hub.Publish(map[string]registry.Service{svc: service(&backplanev1.Manifest{
		Service: svc, Version: "1.0.0", Workflows: []*backplanev1.Workflow{{Name: "Greet"}},
	})})

	o := ops.Detached(hub, ops.WithTemporal(func() (client.Client, error) { return c, nil }, svc))
	api := o.Schedules()

	t.Cleanup(func() {
		for _, name := range []string{"collect", "manual"} {
			_ = c.ScheduleClient().GetHandle(context.Background(), svc+"/"+name).Delete(context.Background())
		}
	})

	dir := t.TempDir()
	write := func(body string) {
		t.Helper()

		if err := os.WriteFile(filepath.Join(dir, "schedules.yaml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write(`
schedules:
  - service: ` + svc + `
    name: collect
    workflow: Greet
    input: {name: a}
    timing: {interval: 3600s}
    paused: true
`)

	h := backplanetest.New(t)
	ops.NewScheduleFiles(h.Root(), o, dir, 50*time.Millisecond)
	h.Start()

	get := func(name string) *consolev1.GetScheduleResponse {
		got, err := api.GetSchedule(ctx, &consolev1.GetScheduleRequest{Service: svc, Name: name})
		if err != nil {
			return nil
		}

		return got
	}

	eventually(t, "created", func() bool {
		got := get("collect")

		return got != nil && got.GetSchedule().GetManagedBy() == "schedules.yaml" &&
			got.GetSchedule().GetState().GetPaused() && got.GetDefinition().GetInput() == `{"name":"a"}`
	})

	got := get("collect")

	_, err := api.UpdateSchedule(ctx, &consolev1.UpdateScheduleRequest{
		Service: svc, Name: "collect", Definition: got.GetDefinition(), Revision: got.GetRevision(),
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("console update of a managed schedule: %v", err)
	}

	if _, err := api.DeleteSchedule(ctx, &consolev1.DeleteScheduleRequest{Service: svc, Name: "collect"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("console delete of a managed schedule: %v", err)
	}

	// The operator unpauses; the file changes: the definition follows the
	// file, the state stays the operator's.
	if _, err := api.UnpauseSchedule(ctx, &consolev1.UnpauseScheduleRequest{Service: svc, Name: "collect"}); err != nil {
		t.Fatal(err)
	}

	if _, err := api.CreateSchedule(ctx, &consolev1.CreateScheduleRequest{
		Service: svc, Name: "manual", Paused: true,
		Definition: &consolev1.ScheduleDefinition{Workflow: "Greet", Timing: &consolev1.ScheduleTiming{Interval: durationpb.New(time.Hour)}},
	}); err != nil {
		t.Fatal(err)
	}

	write(`
schedules:
  - service: ` + svc + `
    name: collect
    workflow: Greet
    input: {name: b}
    timing: {interval: 3600s}
    paused: true
  - service: ` + svc + `
    name: manual
    workflow: Greet
    timing: {interval: 7200s}
`)

	eventually(t, "updated and taken over", func() bool {
		collect, manual := get("collect"), get("manual")

		return collect != nil && collect.GetDefinition().GetInput() == `{"name":"b"}` &&
			!collect.GetSchedule().GetState().GetPaused() &&
			manual != nil && manual.GetSchedule().GetManagedBy() == "schedules.yaml"
	})

	write("schedules: []\n")

	eventually(t, "removed", func() bool { return get("collect") == nil && get("manual") == nil })
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()

	for deadline := time.Now().Add(20 * time.Second); !cond(); time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("not reached: %s", what)
		}
	}
}
