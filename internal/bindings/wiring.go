package bindings

import (
	"context"
	"math"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
)

// WiringAPI is the Manager as backplane.console.v1.WiringService: the
// wiring editor's catalog, analysis and refactoring. Nothing here saves.
type WiringAPI struct {
	consolev1.UnimplementedWiringServiceServer

	m *Manager
}

var _ consolev1.WiringServiceServer = WiringAPI{}

// WiringAPI serves the wiring editor.
func (m *Manager) WiringAPI() WiringAPI { return WiringAPI{m: m} }

// GetWiringCatalog implements WiringService: every service's hooks,
// activities and events from its latest manifest.
func (a WiringAPI) GetWiringCatalog(
	context.Context, *consolev1.GetWiringCatalogRequest,
) (*consolev1.GetWiringCatalogResponse, error) {
	cat := a.m.src.Current()
	if cat.Index == 0 {
		return nil, a.m.status(ErrNotSynced)
	}

	out := &consolev1.GetWiringCatalogResponse{}

	for _, name := range sortedKeys(cat.Services) {
		m := cat.Services[name].Latest()
		out.Services = append(out.Services, &consolev1.WiringContract{
			Service: name, Version: m.GetVersion(), Hooks: m.GetHooks(), Activities: m.GetActivities(),
			Events: m.GetEvents(),
		})
	}

	return out, nil
}

// AnalyzeBinding implements WiringService.
func (a WiringAPI) AnalyzeBinding(
	_ context.Context, req *consolev1.AnalyzeBindingRequest,
) (*consolev1.AnalyzeBindingResponse, error) {
	cat, err := a.m.Catalog()
	if err != nil {
		return nil, a.m.status(err)
	}

	analysis, err := AnalyzeBinding(BindingFromPB(req.GetDefinition()), cat)
	if err != nil {
		return nil, a.m.status(err)
	}

	return &consolev1.AnalyzeBindingResponse{Analysis: analysis.PB()}, nil
}

// AnalyzeRule implements WiringService.
func (a WiringAPI) AnalyzeRule(
	_ context.Context, req *consolev1.AnalyzeRuleRequest,
) (*consolev1.AnalyzeRuleResponse, error) {
	cat, err := a.m.Catalog()
	if err != nil {
		return nil, a.m.status(err)
	}

	analysis, err := AnalyzeRule(RuleFromPB(req.GetDefinition()), cat)
	if err != nil {
		return nil, a.m.status(err)
	}

	return &consolev1.AnalyzeRuleResponse{Analysis: analysis.PB()}, nil
}

// RenameStep implements WiringService.
func (a WiringAPI) RenameStep(
	_ context.Context, req *consolev1.RenameStepRequest,
) (*consolev1.RenameStepResponse, error) {
	switch def := req.GetDefinition().(type) {
	case *consolev1.RenameStepRequest_Binding:
		b, vs := RenameBinding(BindingFromPB(def.Binding), req.GetFrom(), req.GetTo())
		if len(vs) > 0 {
			return &consolev1.RenameStepResponse{Violations: ViolationsPB(vs)}, nil
		}

		return &consolev1.RenameStepResponse{Definition: &consolev1.RenameStepResponse_Binding{Binding: b.PB()}}, nil
	case *consolev1.RenameStepRequest_Rule:
		r, vs := RenameRule(RuleFromPB(def.Rule), req.GetFrom(), req.GetTo())
		if len(vs) > 0 {
			return &consolev1.RenameStepResponse{Violations: ViolationsPB(vs)}, nil
		}

		return &consolev1.RenameStepResponse{Definition: &consolev1.RenameStepResponse_Rule{Rule: r.PB()}}, nil
	default:
		return nil, invalid("a binding or rule definition is required")
	}
}

// PB is the analysis as a message.
func (a Analysis) PB() *consolev1.Analysis {
	out := &consolev1.Analysis{Violations: ViolationsPB(a.Violations), Secrets: a.Secrets, Runtime: a.Runtime}

	for i := range a.Steps {
		s := &a.Steps[i]
		out.Steps = append(out.Steps, &consolev1.StepAnalysis{
			Name: s.Name, Level: int32(max(min(s.Level, math.MaxInt32), -1)), //nolint:gosec // bounded
			Data: s.Data, After: s.After, When: s.When, Undo: s.Undo, Parent: s.Parent, ItemType: s.ItemType,
		})
	}

	for _, t := range a.Types {
		out.Types = append(out.Types, &consolev1.ValueType{Path: t.Path, Type: t.Type})
	}

	for _, r := range a.References {
		out.References = append(out.References, &consolev1.Reference{
			Path: r.Path, Variable: r.Variable, Fields: r.Fields, Expr: rangePB(r.Expr),
		})
	}

	return out
}
