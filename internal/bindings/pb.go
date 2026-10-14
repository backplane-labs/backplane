package bindings

import (
	"fmt"
	"math"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
)

// BindingFromPB is the definition a message carries.
func BindingFromPB(m *consolev1.BindingDefinition) Binding {
	return Binding{
		Hook: m.GetHook(), Description: m.GetDescription(), Steps: stepsFromPB(m.GetSteps()),
		Result: ValueOf(m.GetResult()), Runtime: m.GetRuntime(), Editor: editorOf(m.GetEditor()),
	}
}

// PB is the definition as a message.
func (b Binding) PB() *consolev1.BindingDefinition {
	return &consolev1.BindingDefinition{
		Hook: b.Hook, Description: b.Description, Steps: stepsPB(b.Steps), Result: b.Result.PB(),
		Runtime: b.Runtime, Editor: editorOf(b.Editor),
	}
}

// RuleFromPB is the definition a message carries.
func RuleFromPB(m *consolev1.RuleDefinition) Rule {
	return Rule{
		Event: m.GetEvent(), When: m.GetWhen(), Description: m.GetDescription(), Steps: stepsFromPB(m.GetSteps()),
		Runtime: m.GetRuntime(), Editor: editorOf(m.GetEditor()),
	}
}

// PB is the definition as a message.
func (r Rule) PB() *consolev1.RuleDefinition {
	return &consolev1.RuleDefinition{
		Event: r.Event, When: r.When, Description: r.Description, Steps: stepsPB(r.Steps), Runtime: r.Runtime,
		Editor: editorOf(r.Editor),
	}
}

func editorOf(m *consolev1.EditorLayout) *consolev1.EditorLayout {
	if m == nil {
		return nil
	}

	c, _ := proto.Clone(m).(*consolev1.EditorLayout)

	return c
}

func stepsFromPB(in map[string]*consolev1.Step) []Step {
	if len(in) == 0 {
		return nil
	}

	out := make([]Step, 0, len(in))

	for name, s := range in {
		out = append(out, Step{
			Name: name, Activity: s.GetActivity(), Input: ValueOf(s.GetInput()), When: s.GetWhen(),
			After: append([]string(nil), s.GetAfter()...), Undo: s.GetUndo(), UndoInput: ValueOf(s.GetUndoInput()),
			Retry: Retry{
				Attempts:        int(min(s.GetRetry().GetAttempts(), math.MaxInt32)),
				InitialInterval: durationOf(s.GetRetry().GetInitialInterval()),
				MaxInterval:     durationOf(s.GetRetry().GetMaxInterval()),
				Backoff:         s.GetRetry().GetBackoff(),
			},
			StartToClose: durationOf(s.GetStartToClose()), Heartbeat: durationOf(s.GetHeartbeat()),
			Description: s.GetDescription(),
			ForEach:     s.GetForEach(), As: s.GetAs(), Concurrency: int(min(s.GetConcurrency(), math.MaxInt32)),
			OnError: s.GetOnError(), MaxItems: int(min(s.GetMaxItems(), math.MaxInt32)),
			Steps: stepsFromPB(s.GetSteps()), Result: ValueOf(s.GetResult()),
		})
	}

	return sortSteps(out)
}

func stepsPB(in []Step) map[string]*consolev1.Step {
	if len(in) == 0 {
		return nil
	}

	out := make(map[string]*consolev1.Step, len(in))

	for i := range in {
		s := &in[i]
		m := &consolev1.Step{
			Activity: s.Activity, Input: s.Input.PB(), When: s.When,
			After: append([]string(nil), s.After...), Undo: s.Undo, UndoInput: s.UndoInput.PB(),
			StartToClose: durationPB(s.StartToClose), Heartbeat: durationPB(s.Heartbeat), Description: s.Description,
			ForEach: s.ForEach, As: s.As, OnError: s.OnError, Steps: stepsPB(s.Steps), Result: s.Result.PB(),
			Concurrency: uint32(min(max(s.Concurrency, 0), math.MaxInt32)), //nolint:gosec // bounded
			MaxItems:    uint32(min(max(s.MaxItems, 0), math.MaxInt32)),    //nolint:gosec // bounded
		}

		if !s.Retry.IsZero() {
			m.Retry = &consolev1.StepRetry{
				Attempts:        uint32(min(max(s.Retry.Attempts, 0), math.MaxInt32)), //nolint:gosec // bounded
				InitialInterval: durationPB(s.Retry.InitialInterval),
				MaxInterval:     durationPB(s.Retry.MaxInterval),
				Backoff:         s.Retry.Backoff,
			}
		}

		out[s.Name] = m
	}

	return out
}

func durationOf(d *durationpb.Duration) time.Duration {
	if d == nil {
		return 0
	}

	return d.AsDuration()
}

func durationPB(d time.Duration) *durationpb.Duration {
	if d == 0 {
		return nil
	}

	return durationpb.New(d)
}

// PB is the version as a message.
func (v BindingVersion) PB() *consolev1.BindingVersion {
	out := &consolev1.BindingVersion{
		Hook: v.Hook, Version: uint64(max(v.Version, 0)), Deleted: v.Deleted, Author: v.Author, Comment: v.Comment,
		CreatedAt: timestamppb.New(v.CreatedAt), RollbackOf: uint64(max(v.RollbackOf, 0)),
	}
	if !v.Deleted {
		out.Definition = v.Binding.PB()
	}

	return out
}

// PB is the version as a message.
func (v RuleVersion) PB() *consolev1.RuleVersion {
	out := &consolev1.RuleVersion{
		RuleId: v.RuleID.String(), Version: uint64(max(v.Version, 0)), Name: v.Name, Deleted: v.Deleted,
		Author: v.Author, Comment: v.Comment, CreatedAt: timestamppb.New(v.CreatedAt),
		RollbackOf: uint64(max(v.RollbackOf, 0)),
	}
	if !v.Deleted {
		out.Definition = v.Rule.PB()
	}

	return out
}

// PB is the rule as a message.
func (r RuleEntry) PB() *consolev1.Rule {
	return &consolev1.Rule{
		Id: r.ID.String(), Paused: r.Paused, Current: r.Current.PB(), CreatedAt: timestamppb.New(r.CreatedAt),
	}
}

// ViolationsPB are the violations as messages.
func ViolationsPB(vs []Violation) []*consolev1.Violation {
	out := make([]*consolev1.Violation, 0, len(vs))
	for _, v := range vs {
		out = append(out, &consolev1.Violation{Path: v.Path, Code: v.Code, Message: v.Message, Expr: rangePB(v.Expr)})
	}

	return out
}

func rangePB(r Range) *consolev1.Range {
	if r.IsZero() {
		return nil
	}

	return &consolev1.Range{Start: uint32(max(r.Start, 0)), End: uint32(max(r.End, 0))} //nolint:gosec // small
}

// encodeBinding is the stored form of a definition: protojson.
func encodeBinding(b Binding) ([]byte, error) {
	out, err := protojson.Marshal(b.PB())
	if err != nil {
		return nil, fmt.Errorf("bindings: encode %s: %w", b.Hook, err)
	}

	return out, nil
}

func decodeBinding(data []byte) (Binding, error) {
	var m consolev1.BindingDefinition
	if err := protojson.Unmarshal(data, &m); err != nil {
		return Binding{}, fmt.Errorf("bindings: decode: %w", err)
	}

	return BindingFromPB(&m), nil
}

func encodeRule(r Rule) ([]byte, error) {
	out, err := protojson.Marshal(r.PB())
	if err != nil {
		return nil, fmt.Errorf("bindings: encode rule: %w", err)
	}

	return out, nil
}

func decodeRule(data []byte) (Rule, error) {
	var m consolev1.RuleDefinition
	if err := protojson.Unmarshal(data, &m); err != nil {
		return Rule{}, fmt.Errorf("bindings: decode rule: %w", err)
	}

	return RuleFromPB(&m), nil
}
