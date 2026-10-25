// Package wire is the platform's wire contract as names (design §8, §9,
// §17): the NATS objects, headers and metadata of events, and the Temporal
// names of hooks, activities and schedules. The SDK (pkg/backplane/...)
// and the server (internal/...) both derive their names here, so the two
// sides of the contract cannot drift. It is not public API: services do
// not import it (a service reaching for native clients reads the names in
// the design), and it depends on nothing but the standard library.
package wire

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Names of the NATS objects. Service, event and consumer names become NATS
// name and subject tokens through Token, so any name maps to valid,
// distinct NATS names; the platform's own names ([a-z0-9-] services,
// CamelCase events) pass through unchanged.
const (
	prefix     = "bp"
	dlqToken   = "dlq"
	durableSep = "__"
	// RedriveToken is the subject token of redriven dead letters inside a
	// source stream: bp.<source>._redrive.<subscriber>.<consumer>. Token
	// never produces it (an escape is _ and two upper-case hex digits), so
	// it cannot collide with an event.
	RedriveToken = "_redrive"
	// maxName keeps durable names well inside the server's limits; longer
	// ones are cut and suffixed with a hash of the whole.
	maxName  = 200
	hashLen  = 16
	hexUpper = "0123456789ABCDEF"
)

// ErrEventName: an event name is not "<service>.<Event>".
var ErrEventName = errors.New("event name must be <service>.<Event>")

// Token escapes s into one NATS token: ASCII letters, digits and '-' stay,
// every other byte becomes _XX (upper-case hex). The mapping is injective,
// and an escaped token never contains "__".
func Token(s string) string {
	var b strings.Builder

	b.Grow(len(s))

	for i := range len(s) {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' {
			b.WriteByte(c)

			continue
		}

		b.WriteByte('_')
		b.WriteByte(hexUpper[c>>4])
		b.WriteByte(hexUpper[c&0x0f])
	}

	return b.String()
}

// Untoken reverses Token; ok is false when s is not an escaped token.
func Untoken(s string) (string, bool) {
	var b strings.Builder

	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '_' {
			b.WriteByte(c)

			continue
		}

		if i+2 >= len(s) {
			return "", false
		}

		hi, lo := strings.IndexByte(hexUpper, s[i+1]), strings.IndexByte(hexUpper, s[i+2])
		if hi < 0 || lo < 0 {
			return "", false
		}

		b.WriteByte(byte(hi<<4 | lo)) //nolint:gosec // two hex digits

		i += 2
	}

	return b.String(), true
}

// StreamName of service's events: bp_<service>.
func StreamName(service string) string { return prefix + "_" + Token(service) }

// StreamSubjects of service's stream: bp.<service>.>.
func StreamSubjects(service string) string { return prefix + "." + Token(service) + ".>" }

// Subject of service's event name: bp.<service>.<Event>.
func Subject(service, name string) string { return prefix + "." + Token(service) + "." + Token(name) }

// DLQStreamName of the dead letters of subscriber's reactors:
// bp_dlq_<subscriber>.
func DLQStreamName(subscriber string) string {
	return prefix + "_" + dlqToken + "_" + Token(subscriber)
}

// DLQStreamSubjects of subscriber's dead-letter stream: bp.dlq.<subscriber>.>.
func DLQStreamSubjects(subscriber string) string {
	return prefix + "." + dlqToken + "." + Token(subscriber) + ".>"
}

// DLQSubject of the dead letters of one reactor: bp.dlq.<subscriber>.<consumer>.
func DLQSubject(subscriber, consumer string) string {
	return prefix + "." + dlqToken + "." + Token(subscriber) + "." + Token(consumer)
}

// RedriveSubject is where dead letters of subscriber's reactor consumer
// are published back for that reactor alone: in the source service's
// stream, bp.<source>._redrive.<subscriber>.<consumer>. The reactor's
// consumer filters on it next to its event's subject; no other consumer
// matches it.
func RedriveSubject(source, subscriber, consumer string) string {
	return prefix + "." + Token(source) + "." + RedriveToken + "." + Token(subscriber) + "." + Token(consumer)
}

// Durable name of subscriber's reactor consumer: <subscriber>__<consumer>,
// both escaped; unique because escaped tokens never contain "__". A name
// longer than maxName is cut and ends with a hash of the full name.
func Durable(subscriber, consumer string) string {
	name := Token(subscriber) + durableSep + Token(consumer)
	if len(name) <= maxName {
		return name
	}

	sum := sha256.Sum256([]byte(name))

	return name[:maxName-hashLen-1] + "_" + hex.EncodeToString(sum[:])[:hashLen]
}

// AllEvents is the reactor pattern of every service's events;
// "<service>.*" is the pattern of one service's.
const AllEvents = "*"

// anyName is the event name of a one-service pattern.
const anyName = "*"

// IsPattern reports whether a reactor's event is a pattern: AllEvents or
// "<service>.*".
func IsPattern(event string) bool {
	_, name, ok := strings.Cut(event, ".")

	return event == AllEvents || ok && name == anyName
}

// EventsSubject of every event of service: bp.<service>.*. Redrives
// (bp.<service>._redrive.…) have more tokens and do not match.
func EventsSubject(service string) string { return prefix + "." + Token(service) + "." + anyName }

// ServiceOfStream is the service of an event stream bp_<service>; ok is
// false for dead-letter and foreign streams.
func ServiceOfStream(name string) (string, bool) {
	rest, ok := strings.CutPrefix(name, prefix+"_")
	if !ok || strings.HasPrefix(rest, dlqToken+"_") {
		return "", false
	}

	service, ok := Untoken(rest)
	if !ok || service == "" || Token(service) != rest {
		return "", false
	}

	return service, true
}

// SplitEvent splits a full event name "<service>.<Event>" at its first dot.
func SplitEvent(full string) (string, string, error) {
	service, name, ok := strings.Cut(full, ".")
	if !ok || service == "" || name == "" {
		return "", "", fmt.Errorf("%w, got %q", ErrEventName, full)
	}

	return service, name, nil
}

// EventOfSubject is the full event name "<service>.<Event>" of an event
// subject bp.<service>.<Event>; ok is false for any other subject (a
// redriven dead letter, a foreign subject).
func EventOfSubject(subject string) (string, bool) {
	parts := strings.Split(subject, ".")
	if len(parts) != 3 || parts[0] != prefix || parts[2] == RedriveToken {
		return "", false
	}

	service, ok1 := Untoken(parts[1])
	name, ok2 := Untoken(parts[2])

	if !ok1 || !ok2 || service == "" || name == "" {
		return "", false
	}

	return service + "." + name, true
}

// CloudEvents over the NATS binding (binary mode): attributes and
// extensions are ce-* headers, the payload is the data.
const (
	HeaderSpecVersion = "ce-specversion"
	HeaderID          = "ce-id"
	HeaderSource      = "ce-source"
	HeaderType        = "ce-type"
	HeaderTime        = "ce-time"
	HeaderSubject     = "ce-subject"
	HeaderContentType = "ce-datacontenttype"
	HeaderInstance    = "ce-instance"
	HeaderVersion     = "ce-version"
	// HeaderMIME is the binding's content type header.
	HeaderMIME = "content-type"
	// CEPrefix starts every CloudEvents header.
	CEPrefix = "ce-"
	// SpecVersion is the CloudEvents version events carry.
	SpecVersion = "1.0"
	// ContentJSON is the content type of every payload.
	ContentJSON = "application/json"

	// HeaderError and HeaderConsumer are added to a dead letter: the last
	// handler error and the reactor that gave up.
	HeaderError    = "bp-error"
	HeaderConsumer = "bp-consumer"
	// HeaderDelivered: deliveries before the message was dead-lettered.
	HeaderDelivered = "bp-delivered"
	// HeaderRedriven marks a dead letter published back for its reactor:
	// the dead letter's sequence in the dead-letter stream.
	HeaderRedriven = "bp-redriven"
	// HeaderTest is the CloudEvents extension (ce-bptest) of an event the
	// console published by hand.
	HeaderTest = "ce-bptest"
)

// Metadata of streams and consumers.
const (
	MetaService  = "bp.service"
	MetaBy       = "bp.ensured-by"
	MetaKind     = "bp.kind"
	MetaConsumer = "bp.consumer"
	MetaEvent    = "bp.event"
	ByEmitter    = "emitter"
	BySubscriber = "subscriber"
	KindEvents   = "events"
	KindDead     = "dead-letters"
)
