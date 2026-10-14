package ops_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"go.temporal.io/sdk/client"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/ops"
	"github.com/backplane-labs/backplane/internal/registry"
)

func TestScheduleAdministrationLive(t *testing.T) {
	t.Parallel()
	c := liveTemporal(t)
	other := liveTemporal(t)
	svc := unique("schedadmin")
	ctx := t.Context()
	hub := registry.NewHub()
	hub.Publish(map[string]registry.Service{svc: service(&backplanev1.Manifest{Service: svc, Version: "1.0.0", Workflows: []*backplanev1.Workflow{{Name: "Greet"}}})})
	first := ops.Detached(hub, ops.WithTemporal(func() (client.Client, error) { return c, nil }, svc)).Schedules()
	second := ops.Detached(hub, ops.WithTemporal(func() (client.Client, error) { return other, nil }, svc)).Schedules()

	req := &consolev1.CreateScheduleRequest{Service: svc, Name: "Hourly", Paused: true, Definition: &consolev1.ScheduleDefinition{Workflow: "Greet", Input: `{"name":"scheduled"}`, Timing: &consolev1.ScheduleTiming{Interval: durationpb.New(time.Hour)}}}
	if _, err := first.CreateSchedule(ctx, req); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = c.ScheduleClient().GetHandle(context.Background(), svc+"/Hourly").Delete(context.Background())
	})

	if _, err := second.CreateSchedule(ctx, req); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("duplicate: %v", err)
	}

	got, err := first.GetSchedule(ctx, &consolev1.GetScheduleRequest{Service: svc, Name: "Hourly"})
	if err != nil {
		t.Fatal(err)
	}

	if !got.GetSchedule().GetState().GetPaused() || len(got.GetRevision()) == 0 || got.GetDefinition().GetInput() != req.GetDefinition().GetInput() {
		t.Fatalf("read: %v", got)
	}
	// Both replicas edit the same revision concurrently: exactly one succeeds.
	update := &consolev1.UpdateScheduleRequest{Service: svc, Name: "Hourly", Definition: proto.Clone(got.GetDefinition()).(*consolev1.ScheduleDefinition), Revision: got.GetRevision()}
	update.Definition.Input = `{"name":"changed"}`

	var writers sync.WaitGroup

	results := make(chan error, 2)

	for _, api := range []ops.ScheduleAPI{first, second} {
		writers.Go(func() { _, err := api.UpdateSchedule(ctx, update); results <- err })
	}

	writers.Wait()
	close(results)

	successes := 0

	for err := range results {
		if err == nil {
			successes++
		} else if status.Code(err) != codes.Aborted {
			t.Errorf("conflict should be ABORTED: %v", err)
		}
	}

	if successes != 1 {
		t.Fatalf("concurrent update successes: %d", successes)
	}

	after, err := second.GetSchedule(ctx, &consolev1.GetScheduleRequest{Service: svc, Name: "Hourly"})
	if err != nil {
		t.Fatal(err)
	}

	if !after.GetSchedule().GetState().GetPaused() {
		t.Fatal("edit unpaused schedule")
	}

	if _, err = first.UpdateSchedule(ctx, update); status.Code(err) != codes.Aborted {
		t.Fatalf("stale revision: %v", err)
	}
	// Pagination is scoped to this service and never drops entries between pages.
	for i := range 3 {
		extra := proto.CloneOf(req)

		extra.Name = fmt.Sprintf("Extra%d", i)
		if _, err := first.CreateSchedule(ctx, extra); err != nil {
			t.Fatal(err)
		}

		t.Cleanup(func() {
			_, _ = first.DeleteSchedule(context.Background(), &consolev1.DeleteScheduleRequest{Service: svc, Name: extra.GetName()})
		})
	}

	deadline := time.Now().Add(10 * time.Second)

	for {
		seen := map[string]bool{}

		var token []byte

		for pageNumber := 0; ; pageNumber++ {
			if pageNumber >= 10 {
				t.Fatal("pagination did not terminate")
			}

			page, err := first.ListSchedules(ctx, &consolev1.ListSchedulesRequest{Service: svc, PageSize: 1, PageToken: token})
			if err != nil {
				t.Fatal(err)
			}

			if len(page.GetSchedules()) > 1 {
				t.Fatal("page size ignored")
			}

			for _, entry := range page.GetSchedules() {
				if entry.GetService() != svc || seen[entry.GetId()] {
					t.Fatalf("wrong or repeated schedule: %v", entry)
				}

				seen[entry.GetId()] = true
			}

			token = page.GetNextPageToken()
			if len(token) == 0 {
				break
			}

			if len(seen) > 4 {
				t.Fatal("pagination did not finish")
			}
		}

		if len(seen) == 4 {
			break
		}

		if time.Now().After(deadline) {
			t.Fatalf("missing schedules: %v", seen)
		}

		time.Sleep(100 * time.Millisecond)
	}

	if _, err := first.ListSchedules(ctx, &consolev1.ListSchedulesRequest{Service: svc, PageSize: 51}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("oversized page: %v", err)
	}
	// Discovery and deletion remain available after the service disappears.
	hub.Publish(map[string]registry.Service{})

	list, err := second.ListSchedules(ctx, &consolev1.ListSchedulesRequest{Service: svc})
	if err != nil || len(list.GetSchedules()) != 4 {
		t.Fatalf("orphan hidden: %v %v", list, err)
	}

	if _, err = second.DeleteSchedule(ctx, &consolev1.DeleteScheduleRequest{Service: svc, Name: "Hourly"}); err != nil {
		t.Fatal(err)
	}

	if _, err = first.GetSchedule(ctx, &consolev1.GetScheduleRequest{Service: svc, Name: "Hourly"}); status.Code(err) != codes.NotFound {
		t.Fatalf("deleted schedule: %v", err)
	}
}
