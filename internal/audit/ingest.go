package audit

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

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

	"github.com/backplane-labs/backplane/internal/apperrors"
	"github.com/backplane-labs/backplane/internal/otlprecord"
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
	errors  ErrorSink

	mu   sync.Mutex
	ln   net.Listener
	grpc *grpc.Server
}

// ErrorSink keeps the exception records the listener receives besides
// audit: the error store.
type ErrorSink interface {
	Limits() apperrors.Limits
	Save(ctx context.Context, records []apperrors.Record) (stored, counted int64, err error)
	Count(ctx context.Context, outcome string, n int64)
}

// IngestOption configures NewIngest.
type IngestOption func(*Ingest)

// WithErrors installs the sink of exception records; without one they are
// rejected like any unmarked record.
func WithErrors(sink ErrorSink) IngestOption { return func(in *Ingest) { in.errors = sink } }

// NewIngest serves the audit listener of settings under parent.
func NewIngest(parent deps.Scope, st deps.Dependency[*store.Store], settings Settings, opts ...IngestOption) *Ingest {
	in := &Ingest{Component: deps.NewComponent(parent, "audit-ingest"), store: st, listen: settings.Listen}
	for _, o := range opts {
		o(in)
	}

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

	now := time.Now()
	batch, rejected := Records(req, now)

	stored, err := in.save(ctx, batch)
	if err != nil {
		in.Log().Warn("audit ingest failed", xlog.Err(err))

		return nil, rpcError(codes.Unavailable, "audit database unavailable")
	}

	if in.errors != nil {
		// Exception records are not audit: they go to the error store and
		// do not count as rejected here.
		errs, badErrs := apperrors.Records(req, now, in.errors.Limits())
		if _, _, err := in.errors.Save(ctx, errs); err != nil {
			in.Log().Warn("error ingest failed", xlog.Err(err))

			return nil, rpcError(codes.Unavailable, "error database unavailable")
		}

		in.errors.Count(ctx, "rejected", badErrs)
		rejected = max(rejected-int64(len(errs))-badErrs, 0)
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

	severity := otlprecord.SeverityText(record)

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
	return otlprecord.RecordTime(record, now)
}

func bodyText(body *commonpb.AnyValue) (string, error) {
	return otlprecord.BodyText(body) //nolint:wrapcheck // the record's own error
}

func stringAttr(attrs []*commonpb.KeyValue, key string) string {
	return otlprecord.StringAttr(attrs, key)
}

func attributesJSON(attrs []*commonpb.KeyValue) (json.RawMessage, error) {
	return otlprecord.AttributesJSON(attrs) //nolint:wrapcheck // the record's own error
}

func validText(s string) bool { return otlprecord.ValidText(s) }

// errRecordText: the record cannot be stored as PostgreSQL text.
var errRecordText = otlprecord.ErrInvalidText

// cut bounds a field to maxFieldBytes on a rune boundary.
func cut(s string) string { return otlprecord.CutBytes(s, maxFieldBytes) }

func cutRunes(s string, limit int) string { return otlprecord.CutRunes(s, limit) }

var errIngestKeys = errors.New("audit.keys: at most 64, each at least 16 characters")
