// Package bindings is backplane's side of bindings (§7.1) and rules
// (§8.1): the model, validation against the manifests, versions in
// PostgreSQL and the console's BindingService, RuleService and
// WiringService.
//
// A binding implements a hook ("<service>.<Hook>") with steps — calls of
// activities of any service — and a result; a rule runs the same steps
// on an event. Values are JSON trees whose strings are CEL expressions
// over JSON values: `req` (the hook's input) or `event` and `meta` (a
// rule's event and its CloudEvents attributes), the output of every step
// by its name, and `steps.<name>.skipped`.
//
// For the executor and the rules engine the package offers:
//
//   - Manager.Active / Manager.ActiveRules: what is in force now;
//   - Compile: a definition checked against a Catalog into a *Program —
//     steps with their dependencies and groups, options resolved from the
//     manifests, expressions compiled; JSON-serializable, so a workflow
//     receives it as input;
//   - Program's evaluation (Start, Bind, When, Input, UndoInput, Result,
//     Match, Eval): pure and deterministic — no clock, no randomness, no
//     I/O, map iteration in key order — so it runs inside workflow code.
package bindings

import (
	"regexp"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
)

// Binding is the definition of a hook's binding.
type Binding struct {
	// Hook is the full hook name "<service>.<Hook>".
	Hook        string
	Description string
	// Steps are sorted by name: order is not significant, execution
	// follows dependencies.
	Steps []Step
	// Result is the hook's output; zero: {}.
	Result Value
	// Runtime runs the binding; unspecified: the installation's default.
	Runtime consolev1.Runtime
	// Editor is the graph editor's layout, kept as saved; compile and
	// execution ignore it.
	Editor *consolev1.EditorLayout
}

// Rule is the definition of a rule.
type Rule struct {
	// Event is the full event name "<service>.<Event>".
	Event string
	// When is a CEL bool over `event` and `meta`; empty: always.
	When        string
	Description string
	// Steps are sorted by name, as Binding.Steps.
	Steps []Step
	// Runtime runs the rule; unspecified: the installation's default.
	Runtime consolev1.Runtime
	Editor  *consolev1.EditorLayout
}

// Step is one call of an activity.
type Step struct {
	// Name is an identifier unique in the definition (its key): the CEL
	// variable of the step's output.
	Name string
	// Activity is the full activity name "<service>.<Activity>".
	Activity string
	// Input of the activity; zero: {}.
	Input Value
	// When is a CEL bool: the step runs only when true; empty: always.
	When string
	// After names steps this one runs after besides those its expressions
	// reference.
	After []string
	// Undo is the compensating activity (full name) run when a later step
	// fails; empty: none.
	Undo string
	// UndoInput is the undo activity's input; zero: the step's output.
	UndoInput Value
	// Retry: zero fields take the activity's defaults, then the platform's.
	Retry Retry
	// StartToClose bounds one attempt; 0: the activity's default, then the
	// platform's.
	StartToClose time.Duration
	// Heartbeat: an attempt silent this long is lost; 0: the activity's
	// default, else none.
	Heartbeat   time.Duration
	Description string

	// ForEach is a CEL list or map: the step runs once per item. Empty: once.
	ForEach string
	// As names the item variable; empty: "item". Its position is
	// "<As>Index".
	As string
	// Concurrency bounds the items in flight; 0: DefaultConcurrency.
	Concurrency int
	// OnError is OnErrorFail (default, also empty) or OnErrorContinue.
	OnError string
	// MaxItems bounds the items; 0: DefaultMaxItems.
	MaxItems int
	// Steps are the body of a for-each step as a sub-flow, sorted by name;
	// exclusive with Activity.
	Steps []Step
	// Result is each item's result when the body is Steps; zero: {}.
	Result Value
}

// For-each options.
const (
	OnErrorFail     = "fail"
	OnErrorContinue = "continue"
	// DefaultItemVar names the item without As.
	DefaultItemVar     = "item"
	DefaultConcurrency = 10
	MaxConcurrency     = 100
	DefaultMaxItems    = 1000
	MaxItemsLimit      = 10000
)

// ItemVar is the name of the item variable of a for-each step.
func (s Step) ItemVar() string {
	if s.As == "" {
		return DefaultItemVar
	}

	return s.As
}

// Retry is a step's retry policy; zero fields are unset.
type Retry struct {
	// Attempts in total, the first included.
	Attempts        int           `json:"attempts"`
	InitialInterval time.Duration `json:"initial_interval"`
	MaxInterval     time.Duration `json:"max_interval"`
	// Backoff multiplies the interval after each attempt (>= 1).
	Backoff float64 `json:"backoff"`
}

// IsZero reports whether no field is set.
func (r Retry) IsZero() bool { return r == Retry{} }

// Equal reports whether two definitions say the same.
func (b Binding) Equal(o Binding) bool { return proto.Equal(b.PB(), o.PB()) }

// Equal reports whether two definitions say the same.
func (r Rule) Equal(o Rule) bool { return proto.Equal(r.PB(), o.PB()) }

// BindingVersion is one saved version of a hook's binding.
type BindingVersion struct {
	Hook    string
	Version int64
	// Binding is the definition; zero on a tombstone.
	Binding Binding
	// Deleted marks a tombstone: the delete of the binding.
	Deleted    bool
	Author     string
	Comment    string
	CreatedAt  time.Time
	RollbackOf int64 // 0: not a rollback
}

// RuleVersion is one saved version of a rule.
type RuleVersion struct {
	RuleID  uuid.UUID
	Version int64
	Name    string
	// Rule is the definition; zero on a tombstone.
	Rule Rule
	// Deleted marks a tombstone: the delete of the rule.
	Deleted    bool
	Author     string
	Comment    string
	CreatedAt  time.Time
	RollbackOf int64 // 0: not a rollback
}

// RuleEntry is a rule with its current version.
type RuleEntry struct {
	ID uuid.UUID
	// Paused: no runs start.
	Paused    bool
	CreatedAt time.Time
	UpdatedAt time.Time
	Current   RuleVersion
}

// Active reports whether the rule runs: not deleted, not paused.
func (r RuleEntry) Active() bool { return !r.Current.Deleted && !r.Paused }

// Violation is one reason a definition is rejected.
type Violation struct {
	// Path is the JSON Pointer of the place in the definition
	// ("/steps/send/input/to"); empty for the whole definition.
	Path    string
	Code    string
	Message string
	// Expr is where in the expression at Path the problem is; zero when
	// it is the place itself.
	Expr Range
}

// Range is a span of an expression's text in Unicode code points; End is
// exclusive.
type Range struct {
	Start int
	End   int
}

// IsZero reports whether the range is unset.
func (r Range) IsZero() bool { return r == Range{} }

func (v Violation) String() string {
	if v.Path == "" {
		return v.Message
	}

	return v.Path + ": " + v.Message
}

// Violation codes.
const (
	CodeInvalidName     = "INVALID_NAME"
	CodeReservedName    = "RESERVED_NAME"
	CodeUnknownHook     = "UNKNOWN_HOOK"
	CodeUnknownEvent    = "UNKNOWN_EVENT"
	CodeUnknownActivity = "UNKNOWN_ACTIVITY"
	CodeUnknownStep     = "UNKNOWN_STEP"
	CodeCycle           = "CYCLE"
	CodeUndoReference   = "UNDO_REFERENCE"
	CodeCEL             = "CEL_ERROR"
	CodeCost            = "COST"
	CodeTypeMismatch    = "TYPE_MISMATCH"
	CodeMissingField    = "MISSING_FIELD"
	CodeUnknownField    = "UNKNOWN_FIELD"
	CodeInvalidValue    = "INVALID_VALUE"
	CodeInvalidOption   = "INVALID_OPTION"
	// CodeRuntimeUnsupported: the step uses what the definition's runtime
	// does not run (undo, retry, heartbeat or a child workflow on direct).
	CodeRuntimeUnsupported = "RUNTIME_UNSUPPORTED"
)

// Variables every expression may see besides the steps: step names must
// differ from them.
const (
	VarReq   = "req"
	VarEvent = "event"
	VarMeta  = "meta"
	VarSteps = "steps"
)

var (
	identRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	// fullNameRE is "<service>.<Name>" (§17).
	fullNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]*\.[A-Z][A-Za-z0-9]*$`)
)

// reserved are names a step may not take: the variables, and CEL's
// reserved words and literals.
//
//nolint:gochecknoglobals // immutable set
var reserved = map[string]bool{
	VarReq: true, VarEvent: true, VarMeta: true, VarSteps: true,
	"true": true, "false": true, "null": true, "in": true, "as": true, "break": true, "const": true,
	"continue": true, "else": true, "for": true, "function": true, "if": true, "import": true,
	"let": true, "loop": true, "package": true, "namespace": true, "return": true, "var": true,
	"void": true, "while": true, "has": true, "dyn": true, "int": true, "uint": true, "double": true,
	"bool": true, "string": true, "bytes": true, "list": true, "map": true, "type": true,
	"null_type": true, "timestamp": true, "duration": true, "optional": true, "math": true,
	"strings": true, "base64": true, "lists": true, "on": true, "when": true,
}

// IsIdent reports whether s is an identifier: [A-Za-z_][A-Za-z0-9_]*.
func IsIdent(s string) bool { return identRE.MatchString(s) }

// IsFullName reports whether s is "<service>.<Name>" (§17).
func IsFullName(s string) bool { return fullNameRE.MatchString(s) }

// SplitName splits "<service>.<Name>" at its first dot into the service
// and the name.
func SplitName(full string) (string, string) {
	for i := range len(full) {
		if full[i] == '.' {
			return full[:i], full[i+1:]
		}
	}

	return "", full
}
