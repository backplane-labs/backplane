package executor

import (
	"errors"

	"github.com/nexus-rpc/sdk-go/nexus"
	"go.temporal.io/sdk/temporal"
)

// readable is what the other side said: the deepest application error
// message in err's chain and its type, under the wrappers Temporal adds
// (activity, child workflow, Nexus); else the deepest other message.
func readable(err error) (string, string) {
	var msg, typ, other string

	for e := err; e != nil; e = errors.Unwrap(e) {
		switch link := e.(type) { //nolint:errorlint // walking the chain
		case *temporal.ApplicationError:
			if link.Message() != "" {
				msg = link.Message()
			}

			if link.Type() != "" {
				typ = link.Type()
			}
		case *temporal.ActivityError, *temporal.ChildWorkflowExecutionError, *temporal.WorkflowExecutionError,
			*temporal.NexusOperationError:
		case *temporal.TimeoutError:
			other = "timed out (" + link.TimeoutType().String() + ")"
		case *temporal.CanceledError:
			other = outcomeCanceled
		case *nexus.HandlerError:
			if link.Message != "" {
				other = link.Message
			}
		default:
			if m := e.Error(); m != "" {
				other = m
			}
		}
	}

	switch {
	case msg != "":
		return msg, typ
	case other != "":
		return other, typ
	default:
		return "failed", typ
	}
}
