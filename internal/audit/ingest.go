package audit

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"

	"github.com/gopherex/xlog"

	"github.com/backplane-labs/backplane/internal/store"
	"github.com/backplane-labs/backplane/internal/store/db"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
)

// Attributes of an application audit record (backplane.AuditLabel and the
// rest in the SDK name the same keys).
const (
	LabelOrigin  = "backplane.audit.origin"
	LabelAudit   = "backplane.audit"
	LabelID      = "backplane.audit.id"
	LabelActor   = "backplane.audit.actor"
	LabelSubject = "backplane.audit.subject"
	LabelOutcome = "backplane.audit.outcome"
	attrEvent    = "event.name"
	attrService  = "service.name"
)

const (
	maxActionRunes  = 256
	maxFieldBytes   = 1024
	maxRecordBytes  = 64 << 10
	maxIngestBytes  = 8 << 20
	unknownService  = "unknown_service"
	keyBytes        = 16
	rejectedMessage = "unmarked, oversized or invalid audit records are not stored"
)

// Ingest is backplane's OTLP audit listener: the deployment's Collector
// exports the log records marked backplane.audit=true here
// (opentelemetry.proto.collector.logs.v1.LogsService over gRPC). Every
// record it accepts is stored before the call returns, so a failed call is
// the Collector's to retry; retries are deduplicated.
type Ingest struct {
	collogspb.UnimplementedLogsServiceServer
	deps.Component

	store   deps.Dependency[*store.Store]
	listen  string
	keys    [][]byte
	records metric.Int64Counter

	mu   sync.Mutex
	ln   net.Listener
	grpc *grpc.Server
}

// NewIngest serves the audit listener of settings under parent.
func NewIngest(parent deps.Scope, st deps.Dependency[*store.Store], settings Settings) *Ingest {
	in := &Ingest{Component: deps.NewComponent(parent, "audit-ingest"), store: st, listen: settings.Listen}
	for _, key := range settings.Keys {
		in.keys = append(in.keys, []byte(key.Reveal()))
	}

	in.records, _ = in.Meter().Int64Counter("backplane.audit.ingest.records",
		metric.WithDescription("Application audit records received, by outcome: stored, duplicate, rejected"))

	in.OnStart(in.start)
	in.OnStop(in.close)
	in.Go(in.serve)

	return in
}

// Addr is the listener's address once started, nil before.
func (in *Ingest) Addr() net.Addr {
	in.mu.Lock()
	defer in.mu.Unlock()

	if in.ln == nil {
		return nil
	}

	return in.ln.Addr()
}

// Register serves the ingest on registrar (tests; the listener does it itself).
func (in *Ingest) Register(registrar grpc.ServiceRegistrar) {
	collogspb.RegisterLogsServiceServer(registrar, in)
}

func (in *Ingest) start(ctx context.Context) error {
	var lc net.ListenConfig

	ln, err := lc.Listen(ctx, "tcp", in.listen)
	if err != nil {
		return fmt.Errorf("audit listen %s: %w", in.listen, err)
	}

	in.mu.Lock()
	in.ln = ln
	in.grpc = grpc.NewServer(grpc.MaxRecvMsgSize(maxIngestBytes))
	in.Register(in.grpc)
	in.mu.Unlock()

	in.Log().Info("audit ingest listening", xlog.String("addr", ln.Addr().String()))

	return nil
}

func (in *Ingest) close(context.Context) error {
	in.mu.Lock()
	defer in.mu.Unlock()

	if in.grpc != nil {
		in.grpc.GracefulStop()
	}

	if in.ln != nil {
		_ = in.ln.Close() // closed already when the gRPC server served it
	}

	return nil
}

// serve runs the gRPC server until the node stops; calls in flight finish.
func (in *Ingest) serve(ctx context.Context) error {
	in.mu.Lock()
	ln, srv := in.ln, in.grpc
	in.mu.Unlock()

	done := make(chan error, 1)

	go func() { done <- srv.Serve(ln) }()

	select {
	case err := <-done:
		return fmt.Errorf("audit serve: %w", err)
	case <-ctx.Done():
		srv.GracefulStop()
		<-done

		return nil
	}
}

// Export stores the batch's audit records in one transaction.
func (in *Ingest) Export(
	ctx context.Context, req *collogspb.ExportLogsServiceRequest,
) (*collogspb.ExportLogsServiceResponse, error) {
	if !in.authorized(ctx) {
		return nil, rpcError(codes.Unauthenticated, "audit key required")
	}

	if !in.store.Ready() {
		return nil, rpcError(codes.Unavailable, "audit database unavailable")
	}

	batch, rejected := Records(req, time.Now())

	stored, err := in.save(ctx, batch)
	if err != nil {
		in.Log().Warn("audit ingest failed", xlog.Err(err))

		return nil, rpcError(codes.Unavailable, "audit database unavailable")
	}

	in.count(ctx, "stored", stored)
	in.count(ctx, "duplicate", int64(len(batch))-stored)
	in.count(ctx, "rejected", rejected)

	response := &collogspb.ExportLogsServiceResponse{}
	if rejected > 0 {
		response.PartialSuccess = &collogspb.ExportLogsPartialSuccess{
			RejectedLogRecords: rejected, ErrorMessage: rejectedMessage,
		}
	}

	return response, nil
}

func (in *Ingest) count(ctx context.Context, outcome string, n int64) {
	if n > 0 && in.records != nil {
		in.records.Add(ctx, n, metric.WithAttributes(attribute.String("outcome", outcome)))
	}
}

// authorized: no keys configured, or the call carries one as a bearer token.
func (in *Ingest) authorized(ctx context.Context) bool {
	if len(in.keys) == 0 {
		return true
	}

	md, _ := metadata.FromIncomingContext(ctx)
	for _, value := range md.Get("authorization") {
		token, ok := strings.CutPrefix(value, "Bearer ")
		if !ok {
			continue
		}

		for _, key := range in.keys {
			if subtle.ConstantTimeCompare([]byte(token), key) == 1 {
				return true
			}
		}
	}

	return false
}

// save inserts the records in one transaction (duplicates skipped) and
// counts the stored ones.
func (in *Ingest) save(ctx context.Context, batch []db.InsertApplicationAuditParams) (int64, error) {
	if len(batch) == 0 {
		return 0, nil
	}

	st := in.store.Get()

	var stored int64

	err := st.InTx(ctx, func(ctx context.Context) error {
		stored = 0

		for i := range batch {
			n, err := st.Q.InsertApplicationAudit(ctx, batch[i])
			if err != nil {
				return fmt.Errorf("insert application audit: %w", err)
			}

			stored += n
		}

		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("store application audit: %w", err)
	}

	return stored, nil
}

// Records are the rows of the batch's records marked backplane.audit=true
// and within the size budget, and how many were rejected.
func Records(req *collogspb.ExportLogsServiceRequest, now time.Time) ([]db.InsertApplicationAuditParams, int64) {
	var (
		out      []db.InsertApplicationAuditParams
		rejected int64
	)

	for _, logs := range req.GetResourceLogs() {
		resource := logs.GetResource()
		service := stringAttr(resource.GetAttributes(), attrService)

		if service == "" {
			service = unknownService
		}

		resourceJSON, resourceErr := attributesJSON(resource.GetAttributes())

		for _, sl := range logs.GetScopeLogs() {
			for _, record := range sl.GetLogRecords() {
				// Platform records are already committed in control history.
				// Acknowledge Collector echoes without duplicating them.
				if stringAttr(record.GetAttributes(), LabelOrigin) == "platform" {
					continue
				}

				if resourceErr != nil || !marked(record) || proto.Size(record) > maxRecordBytes {
					rejected++

					continue
				}

				entry, err := row(record, resource, service, resourceJSON, now)
				if err != nil {
					rejected++
					continue
				}

				out = append(out, entry)
			}
		}
	}

	return out, rejected
}

func marked(record *logspb.LogRecord) bool {
	for _, kv := range record.GetAttributes() {
		if kv.GetKey() == LabelAudit {
			return kv.GetValue().GetBoolValue()
		}
	}

	return false
}

func row(
	record *logspb.LogRecord, resource *resourcepb.Resource, service string, resourceJSON json.RawMessage, now time.Time,
) (db.InsertApplicationAuditParams, error) {
	attrs := record.GetAttributes()

	body, err := bodyText(record.GetBody())
	if err != nil {
		return db.InsertApplicationAuditParams{}, err
	}

	attributes, err := attributesJSON(attrs)
	if err != nil {
		return db.InsertApplicationAuditParams{}, err
	}

	if !validText(record.GetSeverityText()) || !validText(record.GetEventName()) {
		return db.InsertApplicationAuditParams{}, errRecordText
	}

	action := stringAttr(attrs, attrEvent)
	if action == "" {
		action = record.GetEventName()
	}

	if action == "" {
		action = body
	}

	severity := record.GetSeverityText()
	if severity == "" && record.GetSeverityNumber() != logspb.SeverityNumber_SEVERITY_NUMBER_UNSPECIFIED {
		severity = strings.TrimPrefix(record.GetSeverityNumber().String(), "SEVERITY_NUMBER_")
	}

	return db.InsertApplicationAuditParams{
		ID:         uuid.Must(uuid.NewV7()),
		Key:        recordKey(record, resource, service),
		Time:       recordTime(record, now),
		Service:    cut(service),
		Action:     cutRunes(action, maxActionRunes),
		Actor:      cut(stringAttr(attrs, LabelActor)),
		Subject:    cut(stringAttr(attrs, LabelSubject)),
		Outcome:    cut(stringAttr(attrs, LabelOutcome)),
		Severity:   severity,
		Body:       body,
		Attributes: attributes,
		Resource:   resourceJSON,
		TraceID:    hex.EncodeToString(record.GetTraceId()),
		SpanID:     hex.EncodeToString(record.GetSpanId()),
	}, nil
}

// recordKey deduplicates retries: the service and backplane.audit.id when
// the record has one, else the record and its resource as sent.
func recordKey(record *logspb.LogRecord, resource *resourcepb.Resource, service string) []byte {
	sum := sha256.New()

	if id := stringAttr(record.GetAttributes(), LabelID); id != "" {
		sum.Write([]byte("id\x00" + service + "\x00" + id))
	} else {
		deterministic := proto.MarshalOptions{Deterministic: true}
		encoded, _ := deterministic.Marshal(resource)
		sum.Write(encoded)
		encoded, _ = deterministic.Marshal(record)
		sum.Write(encoded)
	}

	return sum.Sum(nil)[:keyBytes]
}

func recordTime(record *logspb.LogRecord, now time.Time) time.Time {
	for _, nanos := range []uint64{record.GetTimeUnixNano(), record.GetObservedTimeUnixNano()} {
		if nanos > 0 && nanos <= uint64(maxTimestamp.UnixNano()) {
			return time.Unix(0, int64(nanos)).UTC() //nolint:gosec // bounded above
		}
	}

	return now.UTC()
}

//nolint:gochecknoglobals // a constant time
var maxTimestamp = time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC)

func bodyText(body *commonpb.AnyValue) (string, error) {
	if body == nil {
		return "", nil
	}

	if s, ok := body.GetValue().(*commonpb.AnyValue_StringValue); ok {
		if !validText(s.StringValue) {
			return "", errRecordText
		}

		return s.StringValue, nil
	}

	value, err := anyJSON(body)
	if err != nil {
		return "", err
	}

	encoded, err := json.Marshal(value)

	return string(encoded), err
}

func stringAttr(attrs []*commonpb.KeyValue, key string) string {
	for _, kv := range attrs {
		if kv.GetKey() == key {
			return kv.GetValue().GetStringValue()
		}
	}

	return ""
}

func attributesJSON(attrs []*commonpb.KeyValue) (json.RawMessage, error) {
	out := make(map[string]any, len(attrs))
	for _, kv := range attrs {
		if !validText(kv.GetKey()) {
			return nil, errRecordText
		}

		value, err := anyJSON(kv.GetValue())
		if err != nil {
			return nil, err
		}

		out[kv.GetKey()] = value
	}

	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("audit attributes: %w", err)
	}

	return encoded, nil
}

// PostgreSQL text and jsonb cannot store NUL or invalid UTF-8. Reject the
// individual record before starting the batch's transaction.
func validText(s string) bool { return utf8.ValidString(s) && !strings.ContainsRune(s, 0) }

var errRecordText = errors.New("audit record contains invalid PostgreSQL text")

// anyJSON is an OTLP value as JSON: bytes as base64, a key-value list as an
// object.
func anyJSON(v *commonpb.AnyValue) (any, error) {
	switch x := v.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		if !validText(x.StringValue) {
			return nil, errRecordText
		}

		return x.StringValue, nil
	case *commonpb.AnyValue_BoolValue:
		return x.BoolValue, nil
	case *commonpb.AnyValue_IntValue:
		return x.IntValue, nil
	case *commonpb.AnyValue_DoubleValue:
		return jsonNumber(x.DoubleValue), nil
	case *commonpb.AnyValue_BytesValue:
		return base64.StdEncoding.EncodeToString(x.BytesValue), nil
	case *commonpb.AnyValue_ArrayValue:
		out := make([]any, 0, len(x.ArrayValue.GetValues()))
		for _, item := range x.ArrayValue.GetValues() {
			value, err := anyJSON(item)
			if err != nil {
				return nil, err
			}

			out = append(out, value)
		}

		return out, nil
	case *commonpb.AnyValue_KvlistValue:
		return attributesJSON(x.KvlistValue.GetValues())
	default:
		return nil, nil //nolint:nilnil // an unset OTLP AnyValue is JSON null
	}
}

// jsonNumber matches protobuf JSON's representation of non-finite doubles.
func jsonNumber(value float64) any {
	switch {
	case math.IsNaN(value):
		return "NaN"
	case math.IsInf(value, 1):
		return "Infinity"
	case math.IsInf(value, -1):
		return "-Infinity"
	default:
		return value
	}
}

// cut bounds a field to maxFieldBytes on a rune boundary.
func cut(s string) string {
	limit := maxFieldBytes
	if len(s) <= limit {
		return s
	}

	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}

	return s[:limit]
}

func cutRunes(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}

	return string([]rune(s)[:limit])
}

var errIngestKeys = errors.New("audit.keys: at most 64, each at least 16 characters")
