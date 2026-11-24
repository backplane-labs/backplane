package broker

import (
	"sync"
	"time"
)

// position follows how far a reactor got in its source stream, so a
// consumer deleted on the server is recreated where the old one stopped
// instead of at the stream's end: at the oldest message not settled yet,
// else right after the newest one seen. Sequences belong to one stream: a
// stream deleted and created again numbers from 1, so the position records
// which one it is in (its creation time).
type position struct {
	mu     sync.Mutex
	stream time.Time                     // creation of the stream the sequences are in
	base   uint64                        // ack floor of the consumer when consumption started
	high   uint64                        // newest stream sequence delivered
	open   map[uint64]*pendingDeadLetter // delivered, not settled; nil until dead-lettering
}

func newPosition() *position { return &position{open: map[uint64]*pendingDeadLetter{}} }

// started records the consumer's ack floor at the start of a consumption
// in the stream created at stream; a different stream starts the position
// over.
func (p *position) started(floor uint64, stream time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.stream.Equal(stream) {
		p.stream, p.base, p.high, p.open = stream, 0, 0, map[uint64]*pendingDeadLetter{}
	}

	p.base = max(p.base, floor)
}

// in reports whether the position's sequences are those of the stream
// created at stream (or no stream is recorded yet).
func (p *position) in(stream time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.stream.IsZero() || p.stream.Equal(stream)
}

// delivered records an unsettled sequence and returns its pending dead letter.
func (p *position) delivered(seq uint64) *pendingDeadLetter {
	p.mu.Lock()
	defer p.mu.Unlock()

	pending, known := p.open[seq]
	if !known {
		p.open[seq] = nil
	}

	p.high = max(p.high, seq)

	return pending
}

// deadLetter keeps the refusal while JetStream is retrying delivery to this
// instance. Other replicas can reprocess it under the at-least-once contract.
func (p *position) deadLetter(seq uint64, pending pendingDeadLetter) {
	if seq == 0 {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	p.open[seq] = &pending
}

// settled records that seq was acked or terminated.
func (p *position) settled(seq uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	delete(p.open, seq)
}

// resume is the stream sequence a recreated consumer starts at.
func (p *position) resume() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()

	var oldest uint64

	for seq := range p.open {
		if oldest == 0 || seq < oldest {
			oldest = seq
		}
	}

	if oldest != 0 {
		return oldest
	}

	return max(p.base, p.high) + 1
}
