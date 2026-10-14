package apperrors

import (
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/store/db"
)

func cond(field consolev1.ErrorField, op consolev1.ErrorOperator, values ...string) *consolev1.ErrorCondition {
	return &consolev1.ErrorCondition{Field: field, Op: op, Values: values}
}

func filterOf(span time.Duration, conditions ...*consolev1.ErrorCondition) *consolev1.ErrorFilter {
	end := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

	return &consolev1.ErrorFilter{Start: timestamppb.New(end.Add(-span)), End: timestamppb.New(end), Conditions: conditions}
}

func TestCheckFilter(t *testing.T) {
	t.Parallel()

	const (
		isOp     = consolev1.ErrorOperator_ERROR_OPERATOR_IS
		isNotOp  = consolev1.ErrorOperator_ERROR_OPERATOR_IS_NOT
		contains = consolev1.ErrorOperator_ERROR_OPERATOR_CONTAINS
		prefix   = consolev1.ErrorOperator_ERROR_OPERATOR_PREFIX
		exists   = consolev1.ErrorOperator_ERROR_OPERATOR_EXISTS
		notExist = consolev1.ErrorOperator_ERROR_OPERATOR_NOT_EXISTS
	)

	printHex := HexFingerprint(make([]byte, fingerprintBytes))

	w, err := checkFilter(filterOf(time.Hour,
		cond(consolev1.ErrorField_ERROR_FIELD_SERVICE, isOp, "a", "b"), cond(consolev1.ErrorField_ERROR_FIELD_SERVICE, prefix, "c"),
		cond(consolev1.ErrorField_ERROR_FIELD_TYPE, contains, "Timeout"), cond(consolev1.ErrorField_ERROR_FIELD_RELEASE, notExist),
		cond(consolev1.ErrorField_ERROR_FIELD_TRACE_ID, exists), cond(consolev1.ErrorField_ERROR_FIELD_ORIGIN, isNotOp, "otel-log"),
		cond(consolev1.ErrorField_ERROR_FIELD_HANDLED, isNotOp, "true"), cond(consolev1.ErrorField_ERROR_FIELD_FINGERPRINT, isOp, printHex),
		cond(consolev1.ErrorField_ERROR_FIELD_ENVIRONMENT, isOp, "skipped"),
	), consolev1.ErrorField_ERROR_FIELD_ENVIRONMENT)
	if err != nil {
		t.Fatal(err)
	}

	if len(w.ServiceIn) != 2 || w.ServicePrefix[0] != "c" || w.TypeLike[0] != "Timeout" || w.ReleaseExists != -1 || w.TraceExists != 1 ||
		w.OriginNot[0] != OriginOTelLog || w.Handled != -1 || len(w.FingerprintIn) != 1 || len(w.EnvironmentIn) != 0 {
		t.Fatalf("where %+v", w)
	}

	params := db.SearchErrorsParams{PageSize: 3}
	w.fill(&params)

	if params.PageSize != 3 || len(params.ServiceIn) != 2 || params.ReleaseExists != -1 || params.MessageIn == nil || params.FingerprintNot == nil {
		t.Fatalf("filled %+v", params)
	}

	defaulted, err := checkFilter(nil, consolev1.ErrorField_ERROR_FIELD_UNSPECIFIED)
	if err != nil || defaulted.End.Sub(defaulted.Start) != defaultGroupWindow {
		t.Fatal("no filter: the last day", err)
	}

	for name, filter := range map[string]*consolev1.ErrorFilter{
		"no end":        {Start: timestamppb.Now()},
		"too long":      filterOf(32 * 24 * time.Hour),
		"backwards":     {Start: timestamppb.New(time.Now()), End: timestamppb.New(time.Now().Add(-time.Hour))},
		"no op":         filterOf(time.Hour, cond(consolev1.ErrorField_ERROR_FIELD_SERVICE, consolev1.ErrorOperator_ERROR_OPERATOR_UNSPECIFIED, "a")),
		"no values":     filterOf(time.Hour, cond(consolev1.ErrorField_ERROR_FIELD_SERVICE, isOp)),
		"exists values": filterOf(time.Hour, cond(consolev1.ErrorField_ERROR_FIELD_RELEASE, exists, "x")),
		"always set":    filterOf(time.Hour, cond(consolev1.ErrorField_ERROR_FIELD_SERVICE, exists)),
		"no contains":   filterOf(time.Hour, cond(consolev1.ErrorField_ERROR_FIELD_SERVICE, contains, "a")),
		"bad origin":    filterOf(time.Hour, cond(consolev1.ErrorField_ERROR_FIELD_ORIGIN, isOp, "mail")),
		"bad handled":   filterOf(time.Hour, cond(consolev1.ErrorField_ERROR_FIELD_HANDLED, isOp, "maybe")),
		"bad print":     filterOf(time.Hour, cond(consolev1.ErrorField_ERROR_FIELD_FINGERPRINT, isOp, "zz")),
		"print prefix":  filterOf(time.Hour, cond(consolev1.ErrorField_ERROR_FIELD_FINGERPRINT, prefix, printHex)),
		"unknown field": filterOf(time.Hour, cond(consolev1.ErrorField_ERROR_FIELD_UNSPECIFIED, isOp, "a")),
	} {
		if _, err := checkFilter(filter, consolev1.ErrorField_ERROR_FIELD_UNSPECIFIED); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestHistogramStepAndCursors(t *testing.T) {
	t.Parallel()

	for span, want := range map[time.Duration]time.Duration{
		time.Minute: time.Second, time.Hour: 30 * time.Second, 24 * time.Hour: 15 * time.Minute, 31 * 24 * time.Hour: 24 * time.Hour,
	} {
		if got := histogramStep(span); got != want {
			t.Errorf("%v: %v, want %v", span, got, want)
		}
	}

	raw := encodeCursor(pageCursor{Version: 1, Filter: "f", Offset: 50})

	var c pageCursor
	if !decodeCursor(raw, &c) || c.Offset != 50 || c.Filter != "f" {
		t.Fatal("round trip", c)
	}

	if decodeCursor("not base64!", &c) || decodeCursor("", &c) {
		t.Fatal("garbage decodes")
	}

	if filterHash(filterOf(time.Hour), "a") == filterHash(filterOf(time.Hour), "b") {
		t.Fatal("the extra tells cursors of one filter apart")
	}

	if n, err := pageSize(0); err != nil || n != defaultPage {
		t.Fatal(n, err)
	}

	if _, err := pageSize(maxPage + 1); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
}
