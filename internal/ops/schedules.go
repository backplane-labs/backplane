package ops

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	workflowservice "go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/gopherex/xlog"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/wire"
)

// ScheduleAPI is backplane.console.v1.ScheduleService.
type ScheduleAPI struct {
	consolev1.UnimplementedScheduleServiceServer

	o *Ops
}

var _ consolev1.ScheduleServiceServer = ScheduleAPI{}

const scheduleListTimeout = 10 * time.Second

// ListSchedules implements ScheduleService.
func (a ScheduleAPI) ListSchedules(
	ctx context.Context, req *consolev1.ListSchedulesRequest,
) (*consolev1.ListSchedulesResponse, error) {
	if req.GetService() != "" && !serviceName.MatchString(req.GetService()) {
		return nil, a.o.status(ctx, ErrInput)
	}

	c, err := a.o.client()
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	size := req.GetPageSize()
	if size == 0 {
		size = 20
	}

	if size > 50 || len(req.GetPageToken()) > 16<<10 {
		return nil, a.o.status(ctx, ErrInput)
	}

	ctx, cancel := context.WithTimeout(ctx, scheduleListTimeout)
	defer cancel()

	query := ""
	if req.GetService() != "" {
		query = "ScheduleId STARTS_WITH '" + req.GetService() + "/'"
	}

	page, err := c.WorkflowService().ListSchedules(ctx, &workflowservice.ListSchedulesRequest{
		Namespace: a.o.namespace, MaximumPageSize: int32(size), NextPageToken: req.GetPageToken(), Query: query,
	})
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	out := &consolev1.ListSchedulesResponse{NextPageToken: page.GetNextPageToken()}
	for _, entry := range page.GetSchedules() {
		info := scheduleListIdentity(entry.GetScheduleId(), req.GetService())
		if info == nil {
			continue
		}

		a.annotateSchedule(info)

		desc, err := c.ScheduleClient().GetHandle(ctx, info.GetId()).Describe(ctx)

		var missing *serviceerror.NotFound
		if errors.As(err, &missing) {
			continue
		}

		if err != nil {
			return nil, a.o.status(ctx, err)
		}

		info.State = scheduleStatePB(desc)
		info.ManagedBy = managedBy(desc.Memo)
		out.Schedules = append(out.Schedules, info)
	}

	slices.SortFunc(out.GetSchedules(), func(a, b *consolev1.ScheduleInfo) int {
		return strings.Compare(a.GetId(), b.GetId())
	})

	return out, nil
}

func scheduleListIdentity(id, filter string) *consolev1.ScheduleInfo {
	service, name, ok := strings.Cut(id, "/")
	if !ok || !serviceName.MatchString(service) || name == "" || (filter != "" && service != filter) {
		return nil
	}

	return &consolev1.ScheduleInfo{Service: service, Name: name, Id: id}
}

// annotateSchedule retains declarations from older module manifests for display.
func (a ScheduleAPI) annotateSchedule(info *consolev1.ScheduleInfo) {
	if manifests, err := a.o.manifests(info.GetService()); err == nil {
		for _, m := range manifests {
			for _, legacy := range m.GetSchedules() {
				if legacy.GetName() == info.GetName() {
					info.Declared = legacy
				}
			}
		}
	}
}

// nextActions bounds the upcoming action times a schedule's state lists.
const nextActions = 5

func scheduleStatePB(d *client.ScheduleDescription) *consolev1.ScheduleState {
	info := d.Info
	out := &consolev1.ScheduleState{
		ActionCount:         int64(info.NumActions),
		MissedCatchupWindow: int64(info.NumActionsMissedCatchupWindow),
		OverlapSkipped:      int64(info.NumActionsSkippedOverlap),
		Created:             stamp(info.CreatedAt), Updated: stamp(info.LastUpdateAt),
	}

	if st := d.Schedule.State; st != nil {
		out.Paused, out.Note = st.Paused, st.Note
	}

	for _, t := range info.NextActionTimes[:min(len(info.NextActionTimes), nextActions)] {
		out.NextActions = append(out.NextActions, timestamppb.New(t))
	}

	for _, r := range info.RecentActions {
		action := &consolev1.ScheduleAction{ScheduleTime: stamp(r.ScheduleTime), ActualTime: stamp(r.ActualTime)}
		if w := r.StartWorkflowResult; w != nil {
			action.WorkflowId, action.RunId = w.WorkflowID, w.FirstExecutionRunID
		}

		out.RecentActions = append(out.RecentActions, action)
	}

	for _, w := range info.RunningWorkflows {
		out.RunningWorkflowIds = append(out.RunningWorkflowIds, w.WorkflowID)
	}

	if act, ok := d.Schedule.Action.(*client.ScheduleWorkflowAction); ok {
		if name, isName := act.Workflow.(string); isName {
			out.WorkflowType = name
		}
	}

	if p := d.Memo.GetFields()[wire.MemoService]; p != nil {
		out.Owner = strings.Trim(payloadJSON(p), `"`)
	}

	return out
}

// handle is the schedule of service named name.
//
//nolint:ireturn // ScheduleHandle is only an interface
func (a ScheduleAPI) handle(ctx context.Context, service, name string) (client.ScheduleHandle, error) {
	if !serviceName.MatchString(service) || name == "" {
		return nil, fmt.Errorf("%w: service and name are required", ErrInput)
	}

	c, err := a.o.client()
	if err != nil {
		return nil, err
	}

	return c.ScheduleClient().GetHandle(ctx, wire.ScheduleID(service, name)), nil
}

// note is the note of a pause or unpause: the operator's, with the author.
func (a ScheduleAPI) note(ctx context.Context, verb, note string) string {
	if note == "" {
		note = verb + " from the console"
	}

	return note + " (" + a.o.author(ctx) + ")"
}

// Waits of a pause or unpause.
const (
	// settleWait bounds how long a pause or unpause waits to be applied.
	settleWait = 5 * time.Second
	// settlePoll is how often it checks.
	settlePoll = 100 * time.Millisecond
)

// ErrNotSettled: the schedule did not reach the requested state in time.
var ErrNotSettled = errors.New("schedule change not applied yet")

// settle waits until the schedule's paused state is paused. Patches are
// applied asynchronously by the schedule's workflow and a quick pause then
// unpause can otherwise be applied out of order; returning once applied
// makes each call a completed step.
func settle(ctx context.Context, h client.ScheduleHandle, paused bool) error {
	ctx, cancel := context.WithTimeout(ctx, settleWait)
	defer cancel()

	for {
		d, err := h.Describe(ctx)
		if err == nil && d.Schedule.State.Paused == paused {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: paused=%v", ErrNotSettled, paused)
		case <-time.After(settlePoll):
		}
	}
}

// PauseSchedule implements ScheduleService.
func (a ScheduleAPI) PauseSchedule(
	ctx context.Context, req *consolev1.PauseScheduleRequest,
) (*consolev1.PauseScheduleResponse, error) {
	h, err := a.handle(ctx, req.GetService(), req.GetName())
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	note := a.note(ctx, "paused", req.GetNote())
	if err := h.Pause(ctx, client.SchedulePauseOptions{Note: note}); err != nil {
		return nil, a.o.status(ctx, err)
	}

	if err := settle(ctx, h, true); err != nil {
		return nil, a.o.status(ctx, err)
	}

	a.o.audit(ctx, "schedule.pause", h.GetID(), xlog.String("note", note))

	return &consolev1.PauseScheduleResponse{}, nil
}

// UnpauseSchedule implements ScheduleService.
func (a ScheduleAPI) UnpauseSchedule(
	ctx context.Context, req *consolev1.UnpauseScheduleRequest,
) (*consolev1.UnpauseScheduleResponse, error) {
	h, err := a.handle(ctx, req.GetService(), req.GetName())
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	note := a.note(ctx, "unpaused", req.GetNote())
	if err := h.Unpause(ctx, client.ScheduleUnpauseOptions{Note: note}); err != nil {
		return nil, a.o.status(ctx, err)
	}

	if err := settle(ctx, h, false); err != nil {
		return nil, a.o.status(ctx, err)
	}

	a.o.audit(ctx, "schedule.unpause", h.GetID(), xlog.String("note", note))

	return &consolev1.UnpauseScheduleResponse{}, nil
}

// TriggerSchedule implements ScheduleService.
func (a ScheduleAPI) TriggerSchedule(
	ctx context.Context, req *consolev1.TriggerScheduleRequest,
) (*consolev1.TriggerScheduleResponse, error) {
	h, err := a.handle(ctx, req.GetService(), req.GetName())
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	// "Run now" runs: the schedule's own overlap policy (skip by default)
	// would drop it while an earlier action is still running.
	trigger := client.ScheduleTriggerOptions{Overlap: enumspb.SCHEDULE_OVERLAP_POLICY_ALLOW_ALL}
	if err := h.Trigger(ctx, trigger); err != nil {
		return nil, a.o.status(ctx, err)
	}

	a.o.audit(ctx, "schedule.trigger", h.GetID())

	return &consolev1.TriggerScheduleResponse{}, nil
}
