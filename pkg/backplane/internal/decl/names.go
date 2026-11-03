package decl

import (
	"fmt"
	"regexp"
)

// ServiceName is the form of a service name (§17): it becomes a DNS label,
// a NATS subject token and a Temporal task queue.
var ServiceName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// CheckService panics unless service matches ServiceName.
func CheckService(kind, service string) {
	if !ServiceName.MatchString(service) {
		panic(fmt.Sprintf("backplane: %s: service name %q must match %s", kind, service, ServiceName))
	}
}

// CheckName panics unless name is CamelCase — [A-Z][A-Za-z0-9]* — the form
// of hook, activity and workflow names: they are Temporal type and Nexus
// operation names and read as <service>.<Name> everywhere.
func CheckName(kind, name string) {
	if !camel(name) {
		panic(fmt.Sprintf("backplane: %s name %q is not CamelCase ([A-Z][A-Za-z0-9]*, e.g. SendEmail)", kind, name))
	}
}

func camel(name string) bool {
	for i, r := range name {
		switch {
		case r >= 'A' && r <= 'Z':
		case i > 0 && (r >= 'a' && r <= 'z' || r >= '0' && r <= '9'):
		default:
			return false
		}
	}

	return name != ""
}
