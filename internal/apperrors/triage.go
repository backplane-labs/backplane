package apperrors

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/store"
	"github.com/backplane-labs/backplane/internal/store/db"
)

// States of a group as stored.
const (
	StateOpen     = "open"
	StateResolved = "resolved"
	StateMuted    = "muted"
	maxNoteBytes  = 1024
)

// DefaultAuthor acts when no console session made the call.
const DefaultAuthor = "admin"

// WithAuthor names who changes a group's state from the request's context
// (the console session); without it DefaultAuthor.
func WithAuthor(fn func(context.Context) string) Option { return func(s *Service) { s.author = fn } }

//nolint:gochecknoglobals // immutable tables
var (
	stateNames = map[consolev1.ErrorGroupState]string{
		consolev1.ErrorGroupState_ERROR_GROUP_STATE_OPEN:     StateOpen,
		consolev1.ErrorGroupState_ERROR_GROUP_STATE_RESOLVED: StateResolved,
		consolev1.ErrorGroupState_ERROR_GROUP_STATE_MUTED:    StateMuted,
	}
	stateEnums = map[string]consolev1.ErrorGroupState{
		StateOpen:     consolev1.ErrorGroupState_ERROR_GROUP_STATE_OPEN,
		StateResolved: consolev1.ErrorGroupState_ERROR_GROUP_STATE_RESOLVED,
		StateMuted:    consolev1.ErrorGroupState_ERROR_GROUP_STATE_MUTED,
	}
)

// triage is a group's state as every group row carries it.
type triage struct {
	State       string
	StateAt     *time.Time
	StateBy     string
	StateNote   string
	RegressedAt *time.Time
}

// apply sets the state fields of g.
func (t triage) apply(g *consolev1.ErrorGroup) {
	g.State, g.StateBy, g.StateNote = stateEnums[t.State], t.StateBy, t.StateNote
	if t.StateAt != nil {
		g.StateAt = timestamppb.New(*t.StateAt)
	}

	if t.RegressedAt != nil {
		g.RegressedAt = timestamppb.New(*t.RegressedAt)
	}
}

// storedGroup is a group as its own row says, without a range.
func storedGroup(
	fingerprint []byte, service, typ, message string, first, last time.Time, count, stored int64, t triage,
) *consolev1.ErrorGroup {
	g := &consolev1.ErrorGroup{
		Fingerprint: HexFingerprint(fingerprint), Service: service, Type: typ, Message: message,
		FirstSeen: timestamppb.New(first), LastSeen: timestamppb.New(last),
		Count: uint64(max(count, 0)), Stored: uint64(max(stored, 0)),
	}
	t.apply(g)

	return g
}

// stateNames reads the requested states; none is open only.
func statesOf(states []consolev1.ErrorGroupState) ([]string, error) {
	if len(states) == 0 {
		return []string{StateOpen}, nil
	}

	out := make([]string, 0, len(states))

	for _, s := range states {
		name, ok := stateNames[s]
		if !ok {
			return nil, invalid("unknown error group state")
		}

		out = append(out, name)
	}

	return out, nil
}

// SetErrorGroupState implements ErrorService: triage, audited.
func (a API) SetErrorGroupState(
	ctx context.Context, req *consolev1.SetErrorGroupStateRequest,
) (*consolev1.SetErrorGroupStateResponse, error) {
	fingerprint, ok := ParseFingerprint(req.GetFingerprint())
	if !ok || req.GetService() == "" {
		return nil, invalid("a group is a fingerprint and a service")
	}

	state, ok := stateNames[req.GetState()]
	if !ok {
		return nil, invalid("state is open, resolved or muted")
	}

	if len(req.GetNote()) > maxNoteBytes {
		return nil, invalid("note exceeds its limit")
	}

	if !a.s.Ready() {
		return nil, a.status(ctx, errStoreUnavailable)
	}

	st := a.s.store.Get()
	now := time.Now().UTC()
	actor := a.s.author(ctx)

	var row db.SetErrorGroupStateRow

	err := st.InAuditedTx(ctx, func(ctx context.Context) error {
		var err error

		row, err = st.Q.SetErrorGroupState(ctx, db.SetErrorGroupStateParams{
			State: state, At: &now, By: actor, Note: req.GetNote(), Fingerprint: fingerprint, Service: req.GetService(),
		})
		if err != nil {
			return err //nolint:wrapcheck // mapped below
		}

		return st.AuditControl(ctx, actor, "error_group."+state, req.GetFingerprint(), req.GetService(),
			store.AuditDetail{State: state, Note: req.GetNote()})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "error group not found") //nolint:wrapcheck // a status
	}

	if err != nil {
		return nil, a.status(ctx, fmt.Errorf("set error group state: %w", err))
	}

	t := triage{
		State: row.State, StateAt: row.StateAt, StateBy: row.StateBy, StateNote: row.StateNote, RegressedAt: row.RegressedAt,
	}
	group := storedGroup(row.Fingerprint, row.Service, row.Type, row.Message, row.FirstSeen, row.LastSeen,
		row.Count, row.Stored, t)

	return &consolev1.SetErrorGroupStateResponse{Group: group}, nil
}
