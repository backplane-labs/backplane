package broker

import (
	"errors"

	"github.com/backplane-labs/backplane/internal/wire"
)

// Names of the NATS objects (design §8, §17) come from the wire contract
// shared with the server (internal/wire); these are its names here.

// Errors of names.
var (
	// ErrEventName: an event name is not "<service>.<Event>".
	ErrEventName = wire.ErrEventName
	// ErrReserved: the service name is reserved for dead letters.
	ErrReserved = errors.New("broker: service name dlq is reserved for dead letters")
)

// dlqToken is the service name reserved for dead letters.
const dlqToken = "dlq"

// Token escapes s into one NATS token (wire.Token).
func Token(s string) string { return wire.Token(s) }

// StreamName of service's events: bp_<service>.
func StreamName(service string) string { return wire.StreamName(service) }

// StreamSubjects of service's stream: bp.<service>.>.
func StreamSubjects(service string) string { return wire.StreamSubjects(service) }

// Subject of service's event name: bp.<service>.<Event>.
func Subject(service, name string) string { return wire.Subject(service, name) }

// DLQStreamName of the dead letters of subscriber's reactors:
// bp_dlq_<subscriber>.
func DLQStreamName(subscriber string) string { return wire.DLQStreamName(subscriber) }

// DLQStreamSubjects of subscriber's dead-letter stream: bp.dlq.<subscriber>.>.
func DLQStreamSubjects(subscriber string) string { return wire.DLQStreamSubjects(subscriber) }

// DLQSubject of the dead letters of one reactor: bp.dlq.<subscriber>.<consumer>.
func DLQSubject(subscriber, consumer string) string { return wire.DLQSubject(subscriber, consumer) }

// RedriveSubject of one reactor's dead letters published back to it by
// backplane: bp.<source>._redrive.<subscriber>.<consumer>.
func RedriveSubject(source, subscriber, consumer string) string {
	return wire.RedriveSubject(source, subscriber, consumer)
}

// Durable name of subscriber's reactor consumer: <subscriber>__<consumer>.
func Durable(subscriber, consumer string) string { return wire.Durable(subscriber, consumer) }

// SplitEvent splits a full event name "<service>.<Event>" at its first dot.
func SplitEvent(full string) (string, string, error) {
	return wire.SplitEvent(full) //nolint:wrapcheck // wraps ErrEventName
}

// checkService rejects a service whose stream would overlap dead letters.
func checkService(service string) error {
	if Token(service) == dlqToken {
		return ErrReserved
	}

	return nil
}
