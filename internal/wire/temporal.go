package wire

// Temporal names (design §7.2, §9, §17).
const (
	// CallHookWorkflow runs one hook call raised outside workflow code, on
	// the hooks queue of the caller.
	CallHookWorkflow = "backplane.CallHook.v1"
	// HooksSuffix: the Nexus service of <service>'s hooks is
	// <service>.Hooks on endpoint <service>.
	HooksSuffix = ".Hooks"
	// hooksQueueSuffix: hook calls from outside workflow code run on
	// <service>.hooks.
	hooksQueueSuffix = ".hooks"

	// Application error types.
	NoBindingType    = "backplane.NoBinding"
	HookFailedType   = "backplane.HookFailed"
	TimeoutType      = "backplane.Timeout"
	NonRetryableType = "backplane.NonRetryable"

	// MemoSource names who raised a hook call: <service>/<instance>, or
	// console:<session> for a run the console started.
	MemoSource = "source"
	// Search attributes of a hook call, when the namespace has them.
	AttrService = "BpService"
	AttrHook    = "BpHook"

	// MemoService is the owning service of a schedule the SDK manages;
	// MemoFingerprint, on its workflow action, the declaration's hash.
	MemoService     = "backplane.service"
	MemoFingerprint = "backplane.schedule"
	// MemoWiring marks a schedule a wiring file manages: on the schedule
	// (set at creation) and on its workflow action, the file's name.
	MemoWiring = "backplane.wiring"
)

// Queue is the task queue of service: its name.
func Queue(service string) string { return service }

// HooksQueue is the queue of service's hook calls from outside workflow
// code.
func HooksQueue(service string) string { return service + hooksQueueSuffix }

// NexusService of service's hooks: <service>.Hooks on endpoint <service>.
func NexusService(service string) string { return service + HooksSuffix }

// ScheduleID is the id of service's schedule name; every schedule of the
// service has the prefix "<service>/".
func ScheduleID(service, name string) string { return service + "/" + name }
