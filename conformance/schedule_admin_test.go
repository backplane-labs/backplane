package conformance_test

import (
	"context"
	"os/exec"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
)

// Administrative calls travel through the generated platform contract and the
// same cookie session as UI, without a native Temporal client in this helper.
func (w *replicaWorld) schedules(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	name := "HourlyAdmin"
	req := &consolev1.CreateScheduleRequest{Service: "hello", Name: name, Paused: true, Definition: &consolev1.ScheduleDefinition{Workflow: "Report", Input: "{}", Timing: &consolev1.ScheduleTiming{Interval: durationpb.New(time.Hour)}}}

	var created consolev1.CreateScheduleResponse
	if err := m1Call(ctx, w.clients[0], consolev1.ScheduleService_CreateSchedule_FullMethodName, req, &created); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var response consolev1.DeleteScheduleResponse

		_ = m1Call(cleanup, w.clients[1], consolev1.ScheduleService_DeleteSchedule_FullMethodName, &consolev1.DeleteScheduleRequest{Service: "hello", Name: name}, &response)
	})

	var got consolev1.GetScheduleResponse
	if err := m1Call(ctx, w.clients[1], consolev1.ScheduleService_GetSchedule_FullMethodName, &consolev1.GetScheduleRequest{Service: "hello", Name: name}, &got); err != nil {
		t.Fatal(err)
	}

	update := &consolev1.UpdateScheduleRequest{Service: "hello", Name: name, Revision: got.GetRevision(), Definition: proto.CloneOf(got.GetDefinition())}
	update.Definition.Timing = &consolev1.ScheduleTiming{Interval: durationpb.New(2 * time.Hour)}

	var writers sync.WaitGroup

	results := make(chan error, 2)

	for _, cc := range w.clients {
		writers.Go(func() {
			var response consolev1.UpdateScheduleResponse
			results <- m1Call(ctx, cc, consolev1.ScheduleService_UpdateSchedule_FullMethodName, update, &response)
		})
	}

	writers.Wait()
	close(results)

	successes := 0

	for err := range results {
		if err == nil {
			successes++
		} else if m1Code(err) != codes.Aborted {
			t.Fatal(err)
		}
	}

	if successes != 1 {
		t.Fatalf("two replicas accepted %d concurrent schedule edits", successes)
	}

	if err := m1Call(ctx, w.clients[1], consolev1.ScheduleService_GetSchedule_FullMethodName, &consolev1.GetScheduleRequest{Service: "hello", Name: name}, &got); err != nil || !got.GetSchedule().GetState().GetPaused() {
		t.Fatalf("pause lost: %v %v", &got, err)
	}

	var deleted consolev1.DeleteScheduleResponse
	if err := m1Call(ctx, w.clients[1], consolev1.ScheduleService_DeleteSchedule_FullMethodName, &consolev1.DeleteScheduleRequest{Service: "hello", Name: name}, &deleted); err != nil {
		t.Fatal(err)
	}
}

func (w *replicaWorld) browserSchedules(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var response consolev1.DeleteScheduleResponse

		_ = m1Call(ctx, w.clients[1], consolev1.ScheduleService_DeleteSchedule_FullMethodName, &consolev1.DeleteScheduleRequest{Service: "hello", Name: "BrowserSchedule"}, &response)
	})

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "node", "tests/schedule-browser.mjs", w.bases[0]+m1Prefix+"/", w.adminToken)
	cmd.Dir = "../web"

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("schedule browser: %v\n%s", err, output)
	}

	t.Log(string(output))
}
