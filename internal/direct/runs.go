package direct

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/gopherex/xlog"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/redact"
	"github.com/backplane-labs/backplane/internal/store"
	"github.com/backplane-labs/backplane/internal/store/db"
	"github.com/backplane-labs/backplane/internal/wire"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/seal"
)

// Statuses of a run and of its steps in the store.
const (
	statusRunning   = "running"
	statusCompleted = "completed"
	statusFailed    = "failed"
	statusCanceled  = "canceled"
	statusScheduled = "scheduled"
	statusTimedOut  = "timed_out"
)

// Bounds of the store.
const (
	// runQueueSize bounds the run records (begin, finish) waiting;
	// stepQueueSize the step records. Past them, records are dropped (and
	// counted), the run goes on. Steps are the bulk and the first to go:
	// a run whose steps were dropped still begins and ends in the store.
	runQueueSize  = 8192
	stepQueueSize = 32768
	// batchSize bounds the records one transaction writes: the writer
	// takes what is queued, up to this many, and commits once.
	batchSize = 256
	// writeTimeout bounds one write (a batch, or one record).
	writeTimeout = 5 * time.Second
	// expireBatch bounds one retention delete.
	expireBatch = 1000
	defaultPage = 50
	maxPage     = 500
)

// write is one record: a statement on the store's queries.
type write func(ctx context.Context, q *db.Queries) error

// Runs records runs in PostgreSQL and reads them for the console. Writes
// are asynchronous and in order: a run never waits for the database; a
// database that is down loses records, not runs. Claim is the exception:
// the dedup of a rule's event needs the answer.
//
// One writer takes what is queued, up to batchSize records, and commits
// them in one transaction: under load a run's records cost a fraction of
// a commit each instead of one commit each. Run records (begin, finish)
// go before the step records queued at the same time; within each kind
// the order is the order of the calls.
type Runs struct {
	st    deps.Dependency[*store.Store]
	log   *xlog.Logger
	runs  chan write
	steps chan write
	drops func()
	// sealer seals the replies kept (the installation's history
	// encryption); nil: they are kept as they are.
	sealer func() seal.Sealer

	mu sync.Mutex
	// stored are the program hashes this process has written.
	stored map[string]bool
}

// NewRuns creates the recorder over the store; drops is told of every
// record lost to a full queue (nil: none).
func NewRuns(st deps.Dependency[*store.Store], log *xlog.Logger, drops func()) *Runs {
	if drops == nil {
		drops = func() {}
	}

	return &Runs{
		st: st, log: log, drops: drops, sealer: func() seal.Sealer { return nil },
		runs: make(chan write, runQueueSize), steps: make(chan write, stepQueueSize),
		stored: map[string]bool{},
	}
}

// Write runs the writer until ctx ends, then drains what is queued within
// the stop budget.
func (r *Runs) Write(ctx context.Context) error {
	for {
		var (
			first write
			step  bool
		)

		select {
		case first = <-r.runs:
		case first = <-r.steps:
			step = true
		case <-ctx.Done():
			r.drain() //nolint:contextcheck // the stop drains with a budget of its own

			return nil
		}

		r.apply(ctx, r.batch(first, step))
	}
}

// batch is first and what is queued with it, up to batchSize: the run
// records first, then the step records. A step's run record is queued
// before the step (Begin before the run), so the run is in the store or
// in the same transaction before its first step.
func (r *Runs) batch(first write, step bool) []write {
	out := make([]write, 0, batchSize)
	if !step {
		out = append(out, first)
	}

	out = r.take(out, r.runs)

	if step {
		out = append(out, first)
	}

	return r.take(out, r.steps)
}

// take appends what queue holds, without waiting, up to batchSize in all.
func (r *Runs) take(out []write, queue chan write) []write {
	for len(out) < batchSize {
		select {
		case w := <-queue:
			out = append(out, w)
		default:
			return out
		}
	}

	return out
}

func (r *Runs) drain() {
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()

	for len(r.runs)+len(r.steps) > 0 {
		var (
			first write
			step  bool
		)

		select {
		case first = <-r.runs:
		case first = <-r.steps:
			step = true
		default:
			return
		}

		r.apply(ctx, r.batch(first, step))
	}
}

// apply writes a batch in one transaction; a batch that fails is written
// record by record, so one bad record loses itself alone.
func (r *Runs) apply(ctx context.Context, batch []write) {
	if !r.ready() {
		for range batch {
			r.drops()
		}

		return
	}

	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
	defer cancel()

	if len(batch) > 1 {
		err := r.applyTx(wctx, batch)
		if err == nil {
			return
		}

		r.log.Warn("direct run batch not written, writing records one by one", xlog.Int("records", len(batch)), xlog.Err(err))
	}

	queries := r.st.Get().Q

	for _, w := range batch {
		if err := w(wctx, queries); err != nil {
			r.drops()
			r.log.Warn("direct run record lost", xlog.Err(err))
		}
	}
}

func (r *Runs) applyTx(ctx context.Context, batch []write) error {
	trans, err := r.st.Get().Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}

	defer func() { _ = trans.Rollback(ctx) }()

	queries := db.New(trans)

	for _, w := range batch {
		if err := w(ctx, queries); err != nil {
			return err
		}
	}

	if err := trans.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	return nil
}

// ready: the store is there to write to.
func (r *Runs) ready() bool { return !r.st.IsZero() && r.st.Ready() }

// enqueue queues a run record; enqueueStep a step record. A full queue
// drops the record.
func (r *Runs) enqueue(w write) {
	select {
	case r.runs <- w:
	default:
		r.drops()
	}
}

func (r *Runs) enqueueStep(w write) bool {
	select {
	case r.steps <- w:
		return true
	default:
		r.drops()

		return false
	}
}

// Start is what a run begins with.
type Start struct {
	ID      string
	Kind    Kind
	Source  string
	Version int64
	Test    bool
	Caller  string
	// Input is the run's payload, masked by prog before it is stored.
	Input []byte
	// Program is the marshaled program the run runs, ProgramHash its
	// Program.Hash: the run references the program by it.
	Program     []byte
	ProgramHash string
	// Keyed: a hook call with a key. A canceled run is claimed again too,
	// and the unmasked result is kept for the calls that repeat it.
	Keyed bool
	// Lease: the run's end is recorded within it; past it, a run still
	// recorded running is running nowhere and is claimed again or swept.
	Lease time.Duration
}

// program is the record of a run's program: one row per content. A hash
// already stored (by this process) is not sent again; a store that lost
// the write keeps it unknown, so the next run sends it again.
func (r *Runs) program(s Start) write {
	r.mu.Lock()
	known := r.stored[s.ProgramHash]
	r.mu.Unlock()

	if known {
		return nil
	}

	return func(ctx context.Context, q *db.Queries) error {
		params := db.PutDirectProgramParams{Hash: s.ProgramHash, Spec: jsonb(s.Program)}
		if err := q.PutDirectProgram(ctx, params); err != nil {
			return fmt.Errorf("program %s: %w", s.ProgramHash, err)
		}

		r.mu.Lock()
		r.stored[s.ProgramHash] = true
		r.mu.Unlock()

		return nil
	}
}

// Claim records the start of a run, synchronously: nil when it is new, or
// a failed one (or one past its lease) is run again; ErrAlreadyStarted
// when the run of the id completed (or was canceled, for an unkeyed one) —
// the event was handled; ErrRunning when it is running within its lease
// or changed meanwhile — the caller tries again later. The store being
// unavailable is an error: the caller decides whether to run blind.
func (r *Runs) Claim(ctx context.Context, s Start, prog *redact.Program) error {
	if !r.ready() {
		return fmt.Errorf("direct runs: %w", deps.ErrNotReady)
	}

	claimed, err := r.claim(ctx, s, prog)

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation {
		// Its program was expired after this process wrote it: write it
		// again.
		r.mu.Lock()
		delete(r.stored, s.ProgramHash)
		r.mu.Unlock()

		claimed, err = r.claim(ctx, s, prog)
	}

	if err != nil || claimed {
		return err
	}

	return r.refused(ctx, s)
}

// refused is why the run of s was not claimed, by its status now.
func (r *Runs) refused(ctx context.Context, s Start) error {
	row, err := r.st.Get().Q.GetDirectRunStatus(ctx, s.ID)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Expired meanwhile: the next claim makes it anew.
		return fmt.Errorf("%w: %s", ErrRunning, s.ID)
	case err != nil:
		return fmt.Errorf("direct runs: status of %s: %w", s.ID, err)
	case row.Status == statusCompleted, row.Status == statusCanceled && !s.Keyed:
		return fmt.Errorf("%w: %s", ErrAlreadyStarted, s.ID)
	}

	return fmt.Errorf("%w: %s", ErrRunning, s.ID)
}

// foreignKeyViolation is PostgreSQL's foreign_key_violation.
const foreignKeyViolation = "23503"

func (r *Runs) claim(ctx context.Context, s Start, prog *redact.Program) (bool, error) {
	queries := r.st.Get().Q

	if put := r.program(s); put != nil {
		if err := put(ctx, queries); err != nil {
			return false, fmt.Errorf("direct runs: claim %s: %w", s.ID, err)
		}
	}

	_, err := queries.ClaimDirectRun(ctx, db.ClaimDirectRunParams{
		ID: s.ID, Kind: string(s.Kind), Source: s.Source, Version: s.Version, Test: s.Test, Caller: s.Caller,
		Input: jsonb(prog.MaskInput(s.Input)), ProgramHash: s.ProgramHash, LeaseMs: s.Lease.Milliseconds(),
		RerunCanceled: s.Keyed,
	})

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("direct runs: claim %s: %w", s.ID, err)
	}

	return true, nil
}

// Begin records the start of a run that needs no dedup (a hook call, a
// test), asynchronously.
func (r *Runs) Begin(s Start, prog *redact.Program) {
	input := jsonb(prog.MaskInput(s.Input))

	if put := r.program(s); put != nil {
		r.enqueue(put)
	}

	r.enqueue(func(ctx context.Context, q *db.Queries) error {
		_, err := q.ClaimDirectRun(ctx, db.ClaimDirectRunParams{
			ID: s.ID, Kind: string(s.Kind), Source: s.Source, Version: s.Version, Test: s.Test, Caller: s.Caller,
			Input: input, ProgramHash: s.ProgramHash, LeaseMs: s.Lease.Milliseconds(),
		})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("begin %s: %w", s.ID, err)
		}

		return nil
	})
}

// Finish records the end of a run: its result masked by prog, or its
// error; incomplete marks a record missing step records (Recorder.Incomplete).
// reply, the unmasked result, is kept for a keyed call (nil otherwise).
func (r *Runs) Finish(id string, prog *redact.Program, result, reply []byte, err error, incomplete bool) {
	status := statusCompleted
	msg, typ := Readable(err)

	switch {
	case errors.Is(err, context.Canceled):
		status = statusCanceled
	case err != nil:
		status = statusFailed
	}

	masked := jsonb(prog.MaskResult(result))

	r.enqueue(func(ctx context.Context, queries *db.Queries) error {
		kept, err := r.sealReply(ctx, id, reply)
		if err != nil {
			return err
		}

		return queries.FinishDirectRun(ctx, db.FinishDirectRunParams{
			ID: id, Status: status, Result: masked, Reply: kept, Error: msg, ErrorType: typ, Incomplete: incomplete,
		})
	})
}

// sealedReply starts a sealed reply: a JSON reply never starts with it.
const sealedReply byte = 0

// SealReplies seals the replies kept from now on with what sealer returns
// (nil: as they are).
func (r *Runs) SealReplies(sealer func() seal.Sealer) { r.sealer = sealer }

// sealReply is reply as it is kept: sealed, bound to its run, when the
// installation encrypts history.
func (r *Runs) sealReply(ctx context.Context, id string, reply []byte) ([]byte, error) {
	s := r.sealer()
	if s == nil || reply == nil {
		return reply, nil
	}

	sealed, err := s.Seal(ctx, reply, []byte(id))
	if err != nil {
		return nil, fmt.Errorf("direct runs: seal reply of %s: %w", id, err)
	}

	return append([]byte{sealedReply}, sealed...), nil
}

// openReply is a kept reply as it was given.
func (r *Runs) openReply(ctx context.Context, id string, kept []byte) ([]byte, error) {
	if len(kept) == 0 || kept[0] != sealedReply {
		return kept, nil
	}

	s := r.sealer()
	if s == nil {
		return nil, fmt.Errorf("direct runs: reply of %s is sealed and encryption is not configured: %w", id, seal.ErrOpen)
	}

	reply, err := s.Open(ctx, kept[1:], []byte(id))
	if err != nil {
		return nil, fmt.Errorf("direct runs: reply of %s: %w", id, err)
	}

	return reply, nil
}

// RunRecorder is the Recorder of one run that also knows whether every
// step record it was given reached the queue.
type RunRecorder interface {
	Recorder
	// Incomplete: a step record was dropped; the run's record is not the
	// whole run.
	Incomplete() bool
}

// Recorder is the RunRecorder of one run: steps masked by its program.
func (r *Runs) Recorder(id string, prog *redact.Program) RunRecorder { //nolint:ireturn // the runner's interface
	return &recorder{runs: r, id: id, prog: prog}
}

type recorder struct {
	runs *Runs
	id   string
	prog *redact.Program

	mu      sync.Mutex
	seq     int32
	dropped bool
}

func (rc *recorder) Incomplete() bool {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	return rc.dropped
}

// record queues a step record; a full queue is remembered.
func (rc *recorder) record(w write) {
	if !rc.runs.enqueueStep(w) {
		rc.mu.Lock()
		rc.dropped = true
		rc.mu.Unlock()
	}
}

func (rc *recorder) Scheduled(
	label, step, parent string, item int, activity string, input []byte, when time.Time,
) {
	st, _ := rc.prog.Step(label)
	masked := jsonb(st.MaskInput(input))

	var itemPtr *int32
	if item >= 0 && item <= math.MaxInt32 {
		itemPtr = proto.Int32(int32(item))
	}

	rc.mu.Lock()
	rc.seq++
	seq := rc.seq
	rc.mu.Unlock()

	rc.record(func(ctx context.Context, q *db.Queries) error {
		return q.UpsertDirectRunStep(ctx, db.UpsertDirectRunStepParams{
			RunID: rc.id, Label: label, Seq: seq, Step: step, Parent: parent, Item: itemPtr, Activity: activity,
			Status: statusScheduled, Input: masked, ScheduledAt: when,
		})
	})
}

func (rc *recorder) Completed(label string, output []byte, when time.Time) {
	st, _ := rc.prog.Step(label)
	masked := jsonb(st.MaskOutput(output))

	rc.record(func(ctx context.Context, q *db.Queries) error {
		return q.UpsertDirectRunStep(ctx, db.UpsertDirectRunStepParams{
			RunID: rc.id, Label: label, Status: statusCompleted, Output: masked, ClosedAt: &when, ScheduledAt: when,
		})
	})
}

func (rc *recorder) Failed(label, msg, typ string, when time.Time) {
	status := statusFailed

	switch typ {
	case wire.TimeoutType:
		status = statusTimedOut
	case "":
		if msg == canceledMsg {
			status = statusCanceled
		}
	}

	rc.record(func(ctx context.Context, q *db.Queries) error {
		return q.UpsertDirectRunStep(ctx, db.UpsertDirectRunStepParams{
			RunID: rc.id, Label: label, Status: status, Error: msg, ErrorType: typ, ClosedAt: &when, ScheduledAt: when,
		})
	})
}

// jsonb is data as a jsonb value: as is when JSON, else a JSON string of
// it (base64 when not text); nil stays nil (SQL NULL).
func jsonb(data []byte) json.RawMessage {
	switch {
	case len(data) == 0:
		return nil
	case json.Valid(data):
		return json.RawMessage(data)
	}

	quoted, _ := json.Marshal(base64.StdEncoding.EncodeToString(data)) //nolint:errchkjson // a string marshals

	return quoted
}

// Expire deletes the runs started before cutoff, a batch at a time.
func (r *Runs) Expire(ctx context.Context, cutoff time.Time) error {
	if !r.ready() {
		return fmt.Errorf("direct runs: %w", deps.ErrNotReady)
	}

	params := db.ExpireDirectRunsParams{Cutoff: cutoff, BatchSize: expireBatch}
	if err := r.st.Get().Q.ExpireDirectRuns(ctx, params); err != nil {
		return fmt.Errorf("direct runs: expire: %w", err)
	}

	if err := r.st.Get().Q.ExpireDirectPrograms(ctx); err != nil {
		return fmt.Errorf("direct runs: expire programs: %w", err)
	}

	// Programs may be gone: the next run of each writes its own again.
	r.mu.Lock()
	clear(r.stored)
	r.mu.Unlock()

	return nil
}

// unrecordedMsg is the error of a run the sweeper closed.
const unrecordedMsg = "the run's end was not recorded"

// Sweep closes the runs still running past their lease: their end was
// never recorded (the record was lost, or the replica died with the run).
// They end failed and incomplete; how many.
func (r *Runs) Sweep(ctx context.Context) (int64, error) {
	if !r.ready() {
		return 0, fmt.Errorf("direct runs: %w", deps.ErrNotReady)
	}

	n, err := r.st.Get().Q.SweepDirectRuns(ctx, unrecordedMsg)
	if err != nil {
		return 0, fmt.Errorf("direct runs: sweep: %w", err)
	}

	return n, nil
}

// Reply is the kept reply of keyed run id, which completed; ErrRunning
// when it no longer has completed (expired meanwhile) — the caller claims
// it again.
func (r *Runs) Reply(ctx context.Context, id string) ([]byte, error) {
	if !r.ready() {
		return nil, fmt.Errorf("direct runs: %w", deps.ErrNotReady)
	}

	row, err := r.st.Get().Q.GetDirectRunReply(ctx, id)

	switch {
	case errors.Is(err, pgx.ErrNoRows), err == nil && row.Status != statusCompleted:
		return nil, fmt.Errorf("%w: %s", ErrRunning, id)
	case err != nil:
		return nil, fmt.Errorf("direct runs: reply of %s: %w", id, err)
	}

	return r.openReply(ctx, id, row.Reply)
}

// Info is what a run is: for the console's binding and rule run RPCs.
type Info struct {
	ID      string
	Kind    Kind
	Source  string
	Version int64
	Test    bool
}

// Get is one run as the console shows it, with its steps; ErrNoRun.
func (r *Runs) Get(ctx context.Context, id string) (*consolev1.GetRunResponse, []*consolev1.StepRun, Info, error) {
	if !r.ready() {
		return nil, nil, Info{}, fmt.Errorf("direct runs: %w", deps.ErrNotReady)
	}

	queries := r.st.Get().Q

	row, err := queries.GetDirectRun(ctx, id)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, nil, Info{}, fmt.Errorf("%w: %s", ErrNoRun, id)
	case err != nil:
		return nil, nil, Info{}, fmt.Errorf("direct runs: get %s: %w", id, err)
	}

	rows, err := queries.ListDirectRunSteps(ctx, id)
	if err != nil {
		return nil, nil, Info{}, fmt.Errorf("direct runs: steps of %s: %w", id, err)
	}

	out := &consolev1.GetRunResponse{
		Run:    runPB(row.ID, row.Source, row.Version, row.Test, row.Caller, row.Status, row.StartedAt, row.ClosedAt),
		Input:  string(row.Input),
		Result: string(row.Result),
	}
	if row.Status == statusFailed || row.Status == statusCanceled {
		out.Failure, out.FailureType = row.Error, row.ErrorType
	}

	steps := make([]*consolev1.StepRun, 0, len(rows))
	for i := range rows {
		steps = append(steps, stepPB(&rows[i]))
	}

	info := Info{ID: row.ID, Kind: Kind(row.Kind), Source: row.Source, Version: row.Version, Test: row.Test}

	return out, steps, info, nil
}

// ErrNoRun: no run with the id.
var ErrNoRun = errors.New("direct: no such run")

// Page is a page of runs.
type Page struct {
	Runs []*consolev1.Run
	// Next is the token of the next page; empty on the last.
	Next []byte
}

// List is the runs of a source (a hook, or "rule/<id>"), newest first:
// test runs or the others, optionally of one status.
func (r *Runs) List(
	ctx context.Context, kind Kind, source string, test bool, status consolev1.RunStatus, pageSize uint32, token []byte,
) (Page, error) {
	if !r.ready() {
		return Page{}, fmt.Errorf("direct runs: %w", deps.ErrNotReady)
	}

	size := int64(pageSize)
	if size <= 0 {
		size = defaultPage
	}

	size = min(size, maxPage)

	before := cursor{At: time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)}
	if len(token) > 0 {
		if err := json.Unmarshal(token, &before); err != nil {
			return Page{}, fmt.Errorf("direct runs: page token: %w", err)
		}
	}

	rows, err := r.st.Get().Q.ListDirectRuns(ctx, db.ListDirectRunsParams{
		Kind: string(kind), Source: source, Test: test, Status: statusFilter(status),
		BeforeAt: before.At, BeforeID: before.ID, PageSize: size + 1,
	})
	if err != nil {
		return Page{}, fmt.Errorf("direct runs: list: %w", err)
	}

	var page Page

	for i := range rows {
		row := &rows[i]
		if int64(i) == size {
			next, _ := json.Marshal(cursor{At: rows[i-1].StartedAt, ID: rows[i-1].ID}) //nolint:errchkjson // plain struct
			page.Next = next

			break
		}

		page.Runs = append(page.Runs,
			runPB(row.ID, row.Source, row.Version, row.Test, row.Caller, row.Status, row.StartedAt, row.ClosedAt))
	}

	return page, nil
}

// cursor is where a page ends: the next starts below it.
type cursor struct {
	At time.Time `json:"at"`
	ID string    `json:"id"`
}

func statusFilter(s consolev1.RunStatus) string {
	switch s {
	case consolev1.RunStatus_RUN_STATUS_RUNNING:
		return statusRunning
	case consolev1.RunStatus_RUN_STATUS_COMPLETED:
		return statusCompleted
	case consolev1.RunStatus_RUN_STATUS_FAILED, consolev1.RunStatus_RUN_STATUS_TIMED_OUT:
		return statusFailed
	case consolev1.RunStatus_RUN_STATUS_CANCELED:
		return statusCanceled
	default:
		return ""
	}
}

func runStatus(s string) consolev1.RunStatus {
	switch s {
	case statusRunning:
		return consolev1.RunStatus_RUN_STATUS_RUNNING
	case statusCompleted:
		return consolev1.RunStatus_RUN_STATUS_COMPLETED
	case statusFailed:
		return consolev1.RunStatus_RUN_STATUS_FAILED
	case statusCanceled:
		return consolev1.RunStatus_RUN_STATUS_CANCELED
	default:
		return consolev1.RunStatus_RUN_STATUS_UNSPECIFIED
	}
}

func runPB(
	id, source string, version int64, test bool, caller, status string, started time.Time, closed *time.Time,
) *consolev1.Run {
	// A rule's source is "rule/<id>"; its identity "rule:<id>@<version>",
	// as Temporal's memo says it.
	name := source
	if rest, ok := strings.CutPrefix(source, "rule/"); ok {
		name = "rule:" + rest
	}

	identity := name + "@draft"
	if version > 0 {
		identity = name + "@" + strconv.FormatInt(version, 10)
	}

	out := &consolev1.Run{
		WorkflowId: id, RunId: id, WorkflowType: wire.DirectWorkflowType, Status: runStatus(status),
		TaskQueue: wire.DirectQueue, StartTime: timestamppb.New(started),
		Memo: map[string]string{wire.MemoSource: jsonString(caller), wire.MemoBinding: jsonString(identity)},
	}
	if closed != nil {
		out.CloseTime = timestamppb.New(*closed)
	}

	_ = test

	return out
}

func stepPB(s *db.ListDirectRunStepsRow) *consolev1.StepRun {
	out := &consolev1.StepRun{
		Step: s.Step, Parent: s.Parent, Activity: s.Activity, ScheduledTime: timestamppb.New(s.ScheduledAt),
		Input: string(s.Input), Output: string(s.Output), Error: s.Error, ErrorType: s.ErrorType, Attempt: 1,
	}

	if s.Item != nil {
		out.Item = proto.Uint32(uint32(max(*s.Item, 0)))
	}

	if s.ClosedAt != nil {
		out.CloseTime = timestamppb.New(*s.ClosedAt)
	}

	switch s.Status {
	case statusScheduled:
		out.Status = consolev1.StepRunStatus_STEP_RUN_STATUS_SCHEDULED
	case statusCompleted:
		out.Status = consolev1.StepRunStatus_STEP_RUN_STATUS_COMPLETED
	case statusFailed:
		out.Status = consolev1.StepRunStatus_STEP_RUN_STATUS_FAILED
	case statusTimedOut:
		out.Status = consolev1.StepRunStatus_STEP_RUN_STATUS_TIMED_OUT
	case statusCanceled:
		out.Status = consolev1.StepRunStatus_STEP_RUN_STATUS_CANCELED
	}

	return out
}

func jsonString(s string) string {
	quoted, _ := json.Marshal(s) //nolint:errchkjson // a string marshals

	return string(quoted)
}
