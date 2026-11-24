package env

import "context"

// ActivityInputGuard checks the raw JSON before a typed activity decodes it.
type ActivityInputGuard func(context.Context, string, []byte) error

func (e *Env) SetActivityInputGuard(guard ActivityInputGuard) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.Manifest.Sealed() || e.activityGuard != nil || guard == nil {
		panic("backplane: declare one non-nil activity input guard before Run")
	}

	e.activityGuard = guard
}

func (e *Env) ActivityInputGuard() ActivityInputGuard {
	e.mu.RLock()
	defer e.mu.RUnlock()

	return e.activityGuard
}

// SetWorkerPayloadGuard stores the driver-specific callback without importing its types into the core.
func (e *Env) SetWorkerPayloadGuard(guard any) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.Manifest.Sealed() || e.workerGuard != nil || guard == nil {
		panic("backplane: declare one non-nil worker payload guard before Run")
	}

	e.workerGuard = guard
}

func (e *Env) WorkerPayloadGuard() any {
	e.mu.RLock()
	defer e.mu.RUnlock()

	return e.workerGuard
}
