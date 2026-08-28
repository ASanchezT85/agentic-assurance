package gateway

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"agentic-assurance/internal/fleet"
	"agentic-assurance/internal/intent"
)

// Telemetry feeds the analytical plane, off the hot path.
//
// ClickHouse is forbidden from the enforcement path (spec section 33, INV-005): a
// decision must not wait on it, and losing it must not stop one. So the pipeline
// hands an intent over and returns, and everything expensive happens here.
//
// The measured reason this is not inline: the gateway's whole enforcement path is
// 12.5 microseconds at p50, and one HTTP round trip to ClickHouse is three orders of
// magnitude more. Inlining it would not slow the path down, it would replace it.
type Telemetry struct {
	Sink *fleet.Sink

	// Batch is how many intents accumulate before a flush. Batching is the design
	// rather than an optimisation: one request per intent would spend more time on
	// round trips than on work.
	Batch int

	// FlushEvery bounds how long an intent waits in the buffer. Without it a quiet
	// tenant's intents sit unwritten until enough arrive, and the fleet view of a
	// low-volume customer is permanently behind.
	FlushEvery time.Duration

	// MaxBuffered caps memory. Beyond it the oldest entries are dropped and counted:
	// telemetry that grows without bound turns a ClickHouse outage into a gateway
	// outage, which is exactly the coupling this file exists to prevent.
	MaxBuffered int

	Log *slog.Logger

	mu        sync.Mutex
	envelopes []*intent.AgentExecutionEnvelope
	decisions map[string]fleet.Decision
	dropped   int64

	wake chan struct{}
}

func NewTelemetry(sink *fleet.Sink, log *slog.Logger) *Telemetry {
	return &Telemetry{
		Sink:        sink,
		Batch:       500,
		FlushEvery:  5 * time.Second,
		MaxBuffered: 50000,
		Log:         log,
		decisions:   map[string]fleet.Decision{},
		wake:        make(chan struct{}, 1),
	}
}

// Observe buffers one decided intent. It never blocks and never fails.
func (t *Telemetry) Observe(e *intent.AgentExecutionEnvelope, d fleet.Decision) {
	if t == nil || t.Sink == nil || e == nil {
		return
	}

	t.mu.Lock()
	if len(t.envelopes) >= t.MaxBuffered {
		// Drop the oldest. The newest observations are the ones a fleet view is
		// about to be asked for, and dropping those instead would keep a buffer full
		// of history nobody is looking at.
		delete(t.decisions, t.envelopes[0].EnvelopeID)
		t.envelopes = t.envelopes[1:]
		t.dropped++
	}
	t.envelopes = append(t.envelopes, e)
	t.decisions[e.EnvelopeID] = d
	full := len(t.envelopes) >= t.Batch
	t.mu.Unlock()

	if full {
		select {
		case t.wake <- struct{}{}:
		default:
		}
	}
}

// Run flushes on a timer, when a batch fills, and once more on shutdown.
func (t *Telemetry) Run(ctx context.Context) {
	if t == nil || t.Sink == nil {
		return
	}
	ticker := time.NewTicker(t.FlushEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// A final flush with its own deadline, because ctx is already done and
			// the buffered intents would otherwise be lost on every restart.
			flushCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			t.Flush(flushCtx)
			cancel()
			return
		case <-ticker.C:
			t.Flush(ctx)
		case <-t.wake:
			t.Flush(ctx)
		}
	}
}

// Flush writes whatever is buffered.
//
// On failure the batch is put back at the front rather than discarded, so a
// ClickHouse restart costs latency instead of history. It is bounded by MaxBuffered
// like everything else: an outage long enough to overflow the buffer loses the oldest
// observations, and says how many.
func (t *Telemetry) Flush(ctx context.Context) {
	if t == nil || t.Sink == nil {
		return
	}

	t.mu.Lock()
	envelopes, decisions, dropped := t.envelopes, t.decisions, t.dropped
	t.envelopes, t.decisions, t.dropped = nil, map[string]fleet.Decision{}, 0
	t.mu.Unlock()

	if len(envelopes) == 0 {
		return
	}
	if dropped > 0 {
		t.logger().Warn("telemetry observations dropped",
			"count", dropped,
			"consequence", "the fleet view of this window undercounts")
	}

	if err := t.Sink.InsertIntents(ctx, envelopes, decisions); err != nil {
		t.requeue(envelopes, decisions)
		t.logger().Error("intent telemetry not written", "err", err,
			"buffered", len(envelopes))
		return
	}
	if err := t.Sink.InsertDependencies(ctx, envelopes); err != nil {
		// The intents landed; only the dependency rows did not. Requeuing the whole
		// batch would write those intents twice, and a duplicated intent inflates
		// every count in a store with no deduplication. Feed coverage degrades for
		// this window instead, which the Coverage type already reports.
		t.logger().Error("dependency telemetry not written", "err", err,
			"consequence", "feed and model coverage for this window will read low")
	}
}

func (t *Telemetry) requeue(envelopes []*intent.AgentExecutionEnvelope, decisions map[string]fleet.Decision) {
	t.mu.Lock()
	defer t.mu.Unlock()

	room := t.MaxBuffered - len(t.envelopes)
	if room <= 0 {
		t.dropped += int64(len(envelopes))
		return
	}
	if len(envelopes) > room {
		t.dropped += int64(len(envelopes) - room)
		envelopes = envelopes[len(envelopes)-room:]
	}
	for id, d := range decisions {
		t.decisions[id] = d
	}
	t.envelopes = append(envelopes, t.envelopes...)
}

func (t *Telemetry) logger() *slog.Logger {
	if t.Log != nil {
		return t.Log
	}
	return slog.Default()
}

// Buffered reports how many observations are waiting. For tests and for an operator
// asking whether telemetry is keeping up.
func (t *Telemetry) Buffered() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.envelopes)
}
