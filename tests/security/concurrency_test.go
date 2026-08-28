package security

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"agentic-assurance/internal/authority"
	"agentic-assurance/internal/execution"
	"agentic-assurance/internal/fleet"
	"agentic-assurance/internal/gateway"
	"agentic-assurance/internal/intent"
)

// The shared mutable state, exercised concurrently.
//
// The race detector ran on this repository for the first time in the fifth audit pass
// and found nothing, which was worth very little: it only reports races on code that
// actually runs concurrently while it watches, and seven of the nine structures with a
// mutex or an atomic had no test that ran them from more than one goroutine. A clean
// race report over sequential tests says the tests are sequential.
//
// Run under the detector with `make test-race`, which runs it in a container because
// -race needs cgo and this development environment has no C compiler. Without that,
// these tests still catch deadlocks and lost updates; they do not catch races.

const (
	concurrentGoroutines = 16
	concurrentIterations = 64
)

// hammer runs fn from many goroutines and waits.
func hammer(t *testing.T, fn func(worker, iteration int)) {
	t.Helper()

	var wg sync.WaitGroup
	start := make(chan struct{})

	for worker := range concurrentGoroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := range concurrentIterations {
				fn(worker, i)
			}
		}()
	}

	// Released together, so the goroutines contend rather than queue politely behind
	// each other's startup.
	close(start)

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the workers did not finish in 30 seconds; something is deadlocked")
	}
}

// The usage ledger is read on every authority evaluation and written after every
// submission, from whichever request goroutine got there.
func TestUsageLedgerUnderConcurrency(t *testing.T) {
	usage := authority.NewMemoryUsage()
	at := time.Now().UTC()
	ctx := context.Background()

	hammer(t, func(worker, i int) {
		key := fmt.Sprintf("idem-%d-%d", worker, i)
		if err := usage.Record(ctx, authority.Entry{
			TenantID: "tenant_c", GrantID: "grant_c", IdempotencyKey: key,
			Notional: 10, SubmittedAt: at, Open: true,
		}); err != nil {
			t.Errorf("record: %v", err)
		}
		if _, err := usage.Usage(ctx, "tenant_c", "grant_c", at); err != nil {
			t.Errorf("usage: %v", err)
		}
		if i%4 == 0 {
			if err := usage.Close(ctx, "tenant_c", key, at); err != nil {
				t.Errorf("close: %v", err)
			}
		}
	})

	snapshot, err := usage.Usage(ctx, "tenant_c", "grant_c", at)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}

	// Every write is a distinct key, so nothing may be lost.
	want := float64(concurrentGoroutines * concurrentIterations * 10)
	if snapshot.Rolling1hNotional != want {
		t.Errorf("rolling = %.0f, want %.0f; %d writes and some did not land",
			snapshot.Rolling1hNotional, want, concurrentGoroutines*concurrentIterations)
	}
}

// The idempotency store decides which of several concurrent claims on one key wins.
// That is the whole reason it exists, and it had no test that made them concurrent
// without a database.
func TestIdempotencyStoreUnderConcurrency(t *testing.T) {
	store := execution.NewMemoryStore()
	ctx := context.Background()
	at := time.Now().UTC()

	var claimed sync.Map

	hammer(t, func(worker, i int) {
		// Deliberately few keys and many workers, so several goroutines contend for
		// the same one.
		key := fmt.Sprintf("idem-shared-%d", i%8)

		_, won, err := store.Claim(ctx, execution.Record{
			TenantID: "tenant_c", IdempotencyKey: key,
			EnvelopeID: "env", ClientOrderID: "coid-" + key,
			State: execution.RecordPending, CreatedAt: at, UpdatedAt: at,
		})
		if err != nil {
			t.Errorf("claim: %v", err)
			return
		}
		if won {
			if _, loaded := claimed.LoadOrStore(key, worker); loaded {
				t.Errorf("two goroutines both won the claim on %s; one idempotency key "+
					"must produce one submission (INV-002)", key)
			}
		}
	})
}

// The parent-intent tracker is written by every request goroutine and prunes as it
// goes, which is a write during a read of the same slice.
func TestParentTrackerUnderConcurrency(t *testing.T) {
	tracker := gateway.NewParentTracker(intent.DefaultClusterConfig)
	at := time.Now().UTC()

	hammer(t, func(worker, i int) {
		n := 100.0
		tracker.Observe(&intent.AgentExecutionEnvelope{
			EnvelopeID: fmt.Sprintf("env-%d-%d", worker, i),
			TenantID:   fmt.Sprintf("tenant_%d", worker%3),
			ReceivedAt: at.Add(time.Duration(i) * time.Millisecond),
			Principal:  intent.Principal{PrincipalID: "prin", AccountID: "acct"},
			Agent:      intent.Agent{AgentID: fmt.Sprintf("agent_%d", worker)},
			Intent: intent.Intent{
				InstrumentID: "instr_us_equity_00206R102",
				AssetClass:   intent.AssetEquity,
				Side:         intent.SideBuy,
				OrderType:    intent.OrderMarket,
				Notional:     &n,
			},
		})
	})
}

// The telemetry buffer is written by every request goroutine and drained by its own
// one, which is the classic producer-consumer shape and had no concurrent test.
func TestTelemetryBufferUnderConcurrency(t *testing.T) {
	// A nil sink: Observe returns immediately and nothing is buffered, which would
	// make this test vacuous. A sink that is non-nil but unreachable exercises the
	// buffer and the requeue path instead.
	telemetry := gateway.NewTelemetry(unreachableSink(), nil)
	telemetry.Batch = 8
	telemetry.MaxBuffered = 64
	telemetry.FlushEvery = time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	go telemetry.Run(ctx)
	defer cancel()

	at := time.Now().UTC()
	hammer(t, func(worker, i int) {
		n := 100.0
		telemetry.Observe(&intent.AgentExecutionEnvelope{
			EnvelopeID: fmt.Sprintf("env-%d-%d", worker, i),
			TenantID:   "tenant_c",
			ReceivedAt: at,
			Principal:  intent.Principal{PrincipalID: "prin", AccountID: "acct"},
			Agent:      intent.Agent{AgentID: "agent"},
			Intent: intent.Intent{
				InstrumentID: "instr_us_equity_00206R102",
				AssetClass:   intent.AssetEquity,
				Side:         intent.SideBuy,
				OrderType:    intent.OrderMarket,
				Notional:     &n,
			},
		}, fleetDecision())
	})

	// The cap holds. An unbounded buffer under an outage is how a telemetry problem
	// becomes a gateway problem.
	if buffered := telemetry.Buffered(); buffered > telemetry.MaxBuffered {
		t.Errorf("buffered %d observations with a cap of %d", buffered, telemetry.MaxBuffered)
	}
}

// unreachableSink is a ClickHouse sink pointed at nothing. Flushes fail, the batch is
// requeued, and the buffer is exercised rather than drained on the first tick.
func unreachableSink() *fleet.Sink {
	return fleet.NewSink("http://127.0.0.1:1", "u", "p")
}

func fleetDecision() fleet.Decision {
	return fleet.Decision{AuthorityDecision: "AUTHORITY_OK", PolicyAction: "ALLOW"}
}
