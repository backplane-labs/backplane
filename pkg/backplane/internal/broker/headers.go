package broker

import (
	"context"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel"

	"github.com/backplane-labs/backplane/internal/wire"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
)

// CloudEvents over the NATS binding (binary mode): attributes and
// extensions are ce-* headers, the payload is the data. The names are the
// wire contract's (internal/wire).
const (
	HeaderSpecVersion = wire.HeaderSpecVersion
	HeaderID          = wire.HeaderID
	HeaderSource      = wire.HeaderSource
	HeaderType        = wire.HeaderType
	HeaderTime        = wire.HeaderTime
	HeaderSubject     = wire.HeaderSubject
	HeaderContentType = wire.HeaderContentType
	HeaderInstance    = wire.HeaderInstance
	HeaderVersion     = wire.HeaderVersion
	// HeaderMIME is the binding's content type header.
	HeaderMIME = wire.HeaderMIME

	// HeaderError and HeaderConsumer are added to a dead letter: the last
	// handler error and the reactor that gave up.
	HeaderError    = wire.HeaderError
	HeaderConsumer = wire.HeaderConsumer
	// HeaderDelivered: deliveries before the message was dead-lettered.
	HeaderDelivered = wire.HeaderDelivered

	cePrefix    = wire.CEPrefix
	specVersion = wire.SpecVersion
	contentJSON = wire.ContentJSON
	// natsPrefix marks the server's own headers, never copied to a dead
	// letter.
	natsPrefix = "Nats-"
	// maxError bounds bp-error: headers share the message size limit.
	maxError = 4096
)

// cloudEvent is the metadata of one published event.
type cloudEvent struct {
	ID       string
	Service  string
	Instance string
	Version  string
	Type     string // full event name <service>.<Event>
	Key      string // ce-subject, optional
	Time     time.Time
	// Extensions by attribute name without the ce- prefix; the SDK's own
	// attributes win over them.
	Extensions map[string]string
}

// headers of e, with the trace context of ctx; Nats-Msg-Id = ce-id lets
// JetStream drop a duplicate publish.
func headers(ctx context.Context, e cloudEvent) nats.Header {
	h := nats.Header{}

	for name, v := range e.Extensions {
		h.Set(cePrefix+name, v)
	}

	h.Set(HeaderSpecVersion, specVersion)
	h.Set(HeaderID, e.ID)
	h.Set(HeaderSource, e.Service)
	h.Set(HeaderType, e.Type)
	h.Set(HeaderTime, e.Time.UTC().Format(time.RFC3339Nano))
	h.Set(HeaderContentType, contentJSON)
	h.Set(HeaderMIME, contentJSON)

	if e.Key != "" {
		h.Set(HeaderSubject, e.Key)
	}

	if e.Instance != "" {
		h.Set(HeaderInstance, e.Instance)
	}

	if e.Version != "" {
		h.Set(HeaderVersion, e.Version)
	}

	h.Set(jetstream.MsgIDHeader, e.ID)
	otel.GetTextMapPropagator().Inject(ctx, carrier(h))

	return h
}

// incoming is the metadata of a delivered event: its CloudEvents
// attributes, the extensions without the ce- prefix, the delivery attempt
// and the reactor's consumer.
func incoming(h nats.Header, consumer string, attempt uint64) env.Incoming {
	in := env.Incoming{
		ID: h.Get(HeaderID), Source: h.Get(HeaderSource), Type: h.Get(HeaderType), Subject: h.Get(HeaderSubject),
		Attempt: int(min(attempt, math.MaxInt32)), Consumer: consumer, //nolint:gosec // bounded
	}

	if t, err := time.Parse(time.RFC3339Nano, h.Get(HeaderTime)); err == nil {
		in.Time = t
	}

	for k := range h {
		name, ok := cutPrefixFold(k, cePrefix)
		if !ok || core[name] {
			continue
		}

		if in.Extensions == nil {
			in.Extensions = map[string]string{}
		}

		in.Extensions[name] = h.Get(k)
	}

	return in
}

// core are the CloudEvents context attributes, not extensions.
//
//nolint:gochecknoglobals // constant set
var core = map[string]bool{
	"specversion": true, "id": true, "source": true, "type": true, "time": true, "subject": true,
	"datacontenttype": true, "dataschema": true,
}

// cutPrefixFold is strings.CutPrefix ignoring the prefix's case (header
// names may arrive canonicalized); the rest is lower-cased.
func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) <= len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return "", false
	}

	return strings.ToLower(s[len(prefix):]), true
}

// deadHeaders are the headers of a dead letter: the original ones without
// the server's, plus why and where it failed. id deduplicates retries of
// the same dead letter.
func deadHeaders(orig nats.Header, id, consumer, cause string, delivered uint64) nats.Header {
	h := nats.Header{}

	for k, vs := range orig {
		if strings.HasPrefix(k, natsPrefix) {
			continue
		}

		h[k] = append([]string(nil), vs...)
	}

	if len(cause) > maxError {
		cause = cause[:maxError]
	}

	h.Set(HeaderError, cause)
	h.Set(HeaderConsumer, consumer)
	h.Set(HeaderDelivered, strconv.FormatUint(delivered, 10))
	h.Set(jetstream.MsgIDHeader, id)

	return h
}

// carrier adapts NATS headers to the OpenTelemetry propagator.
type carrier nats.Header

func (c carrier) Get(key string) string { return nats.Header(c).Get(key) }

func (c carrier) Set(key, value string) { nats.Header(c).Set(key, value) }

func (c carrier) Keys() []string {
	out := make([]string, 0, len(c))
	for k := range c {
		out = append(out, k)
	}

	return out
}
