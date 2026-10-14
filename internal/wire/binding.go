package wire

import "strings"

// Names of backplane's executor (design §7.2, §8.1, §17): the workflow that
// runs a binding or a rule, its ids and its failures.
const (
	// BindingWorkflow runs one binding (a hook call) or one rule (an
	// event) on backplane's own task queue. The version is in the name: a
	// change that is not replay-compatible is a new name (.v2), registered
	// next to the old one until runs of the old one are gone.
	BindingWorkflow = "backplane.Binding.v1"

	// BindingRunPrefix starts the id of a hook call's run:
	// binding/<hook>/<Nexus request id>.
	BindingRunPrefix = "binding/"
	// TestRunPrefix starts the id of a console test run:
	// test/<hook>/<uuid>.
	TestRunPrefix = "test/"

	// Application error types of a failed run.
	//
	// StepFailedType: a step's activity (or child workflow) failed after
	// its retries; the message is "step <name>: <service>.<Activity>:
	// <handler's message>".
	StepFailedType = "backplane.StepFailed"
	// TransformFailedType: an expression failed on the run's values
	// ("transform failed at step render (input.to): ..."), or the program
	// or its input could not be read.
	TransformFailedType = "backplane.TransformFailed"

	// MemoBinding is the memo of a run: what ran, "<hook>@<version>"
	// ("@draft" for an unsaved definition) or "rule:<id>@<version>".
	MemoBinding = "backplane.binding"
)

// BindingRunID is the run of a hook call: the Nexus request id makes a
// retried start of the same operation the same run.
func BindingRunID(hook, requestID string) string { return BindingRunPrefix + hook + "/" + requestID }

// TestRunID is a console test run of hook's binding.
func TestRunID(hook, id string) string { return TestRunPrefix + hook + "/" + id }

// BindingRunHook is the hook of a run id made by BindingRunID or TestRunID,
// and whether it is a test run; ok is false for any other id.
func BindingRunHook(workflowID string) (string, bool, bool) {
	rest, isRun := strings.CutPrefix(workflowID, BindingRunPrefix)

	test := false
	if !isRun {
		if rest, test = strings.CutPrefix(workflowID, TestRunPrefix); !test {
			return "", false, false
		}
	}

	hook, _, ok := strings.Cut(rest, "/")
	if !ok || !strings.Contains(hook, ".") {
		return "", false, false
	}

	return hook, test, true
}

// Names of the direct runtime (internal/direct): runs in backplane's own
// process, recorded in PostgreSQL.
const (
	// DirectRunPrefix starts the id of a direct run: direct/binding/<hook>/<id>,
	// direct/rule/<rule id>/<ce-id>, direct/test/<hook or rule>/<uuid>.
	DirectRunPrefix = "direct/"
	// DirectWorkflowType is what a direct run reports as its workflow type.
	DirectWorkflowType = "backplane.Direct.v1"
	// DirectQueue is what a direct run reports as its task queue.
	DirectQueue = "direct"
)

// DirectRunID is the id of a direct run of a hook call.
func DirectRunID(hook, id string) string { return DirectRunPrefix + BindingRunPrefix + hook + "/" + id }

// DirectTestRunID is the id of a direct console test run of hook's binding
// or of a rule ("rule/<id>").
func DirectTestRunID(source, id string) string {
	return DirectRunPrefix + TestRunPrefix + source + "/" + id
}

// IsDirectRun reports whether workflowID names a direct run.
func IsDirectRun(workflowID string) bool { return strings.HasPrefix(workflowID, DirectRunPrefix) }
