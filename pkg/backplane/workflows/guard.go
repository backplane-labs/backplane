package workflows

import (
	commonpb "go.temporal.io/api/common/v1"

	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/decl"
)

// GuardWorkerPayloads installs one guard on the SDK's main Temporal worker,
// before Run. It sees read-only payloads after decryption, before decoding,
// and must be safe to call concurrently. Ignore payloads the guard does not
// know. The guard does not alter encoding, the workflow client, or hook workers.
// A refusal requests a Workflow Task failure on the Temporal paths that support
// it; signal, update arguments, query and memo decoding do not retry the task.
func GuardWorkerPayloads(scope deps.Scope, guard func([]*commonpb.Payload) error) {
	if guard == nil {
		panic("backplane: worker payload guard must not be nil")
	}

	e, _ := decl.Env(scope, "worker payload guard")
	e.SetWorkerPayloadGuard(guard)
}
