//go:build load

package performance

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"

	"agentic-assurance/internal/evidence"
)

// F4-B004: the outbox publishes at least as fast as evidence arrives, for as long as
// evidence keeps arriving.
//
// The previous test appended ten thousand events, then started a publisher, then measured
// how long the backlog took to clear. Every finite backlog clears once the producer stops;
// what that measured was the drain rate, not stability. It also had no publisher of its own
// during the arrival phase, so with a gateway running against the same database the queue
// stayed shallow and with none the same code reached 100% of arrivals in flight — the
// number said more about what else was running than about the outbox.
//
// This one owns both sides. It refuses to run if anything else is draining the database,
// because counting another process's capacity as your own is how a capacity test passes on
// a machine where the property does not hold.
//
//	go test -tags=load -timeout 20m -run TestTheOutboxSustainsProduction ./tests/performance/
//
// Target rate. A sustained submission run on this build produces about nine evidence
// events per decided intent at roughly 250 decisions per second, so the platform's own
// output is about 2,250 events per second. The target below is 2,500/s for 120 seconds —
// at or above what the current build produces — and it is stated here rather than derived
// so that a later change to either number is a visible edit.
const (
	targetRate = 2500
	duration   = 120 * time.Second

	// The supported single-gateway configuration. OUTBOX_WORKERS defaults to 1 in the
	// deployable, and this measures that default unless the environment overrides it.
	defaultWorkers = 1
)

func TestTheOutboxSustainsProduction(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	pool := appPool(t)
	outbox := outboxPool(t)
	store := evidence.NewStore(pool)
	drainer := evidence.NewStore(outbox)

	conn, err := nats.Connect(streamURL(t))
	if err != nil {
		t.Skipf("no NATS at %s: %v", streamURL(t), err)
	}
	defer conn.Close()
	js, err := evidence.EnsureStream(ctx, conn)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}

	tenant := fmt.Sprintf("tenant_cap_%d", time.Now().UnixNano())
	// The canary gets a tenant of its own, so the run's own counts stay exact.
	requireNoOtherPublisher(t, ctx, store, drainer, tenant+"_canary")

	workers := defaultWorkers
	if v := os.Getenv("OUTBOX_WORKERS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			workers = n
		}
	}
	t.Logf("target %d events/s for %s, %d publisher worker(s)", targetRate, duration, workers)

	// The publishers, running for the whole measurement.
	publishing, stopPublishing := context.WithCancel(ctx)
	var publishers sync.WaitGroup
	var published atomic.Int64
	for i := range workers {
		publishers.Add(1)
		go func(i int) {
			defer publishers.Done()
			p := &evidence.OutboxPublisher{
				Store: drainer, Publisher: evidence.NewPublisher(js),
				Batch: 500, Owner: fmt.Sprintf("capacity-%d", i),
			}
			for publishing.Err() == nil {
				n := p.Drain(publishing)
				published.Add(int64(n))
				if n == 0 {
					time.Sleep(20 * time.Millisecond)
				}
			}
		}(i)
	}

	// The producer: several goroutines appending batches, paced to the target.
	const (
		producers = 4
		perBatch  = 125
	)
	var produced atomic.Int64
	producing, stopProducing := context.WithCancel(ctx)
	var writers sync.WaitGroup
	batchesPerSecond := float64(targetRate) / float64(perBatch)
	interval := time.Duration(float64(time.Second) / (batchesPerSecond / float64(producers)))

	start := time.Now()
	for p := range producers {
		writers.Add(1)
		go func(p int) {
			defer writers.Done()
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			seq := 0
			for {
				select {
				case <-producing.Done():
					return
				case <-ticker.C:
				}
				batch := make([]evidence.Event, 0, perBatch)
				for i := range perBatch {
					seq++
					at := time.Now().UTC()
					batch = append(batch, evidence.Event{
						SchemaVersion: evidence.SchemaVersion,
						EventID:       fmt.Sprintf("cap_%d_%d_%d", start.UnixNano(), p, seq),
						EventName:     evidence.AuthorityEvaluated,
						TenantID:      tenant,
						AggregateID:   fmt.Sprintf("env_%d_%d", p, seq),
						CorrelationID: fmt.Sprintf("corr_%d_%d", p, seq),
						OccurredAt:    at,
						ProducedAt:    at,
						Producer:      "assurance-gateway",
						Sequence:      int64(i + 1),
						Payload:       map[string]any{"allowed": true},
					})
				}
				if err := store.AppendBatch(context.Background(), batch); err != nil {
					if producing.Err() == nil {
						t.Errorf("append: %v", err)
					}
					return
				}
				produced.Add(int64(len(batch)))
			}
		}(p)
	}

	// Sampling, every two seconds for the whole run.
	var samples []sample
	sampler := time.NewTicker(2 * time.Second)
	deadline := time.After(duration)

loop:
	for {
		select {
		case <-deadline:
			break loop
		case <-sampler.C:
			depth, oldest, err := drainer.Depth(context.Background(), tenant)
			if err != nil {
				t.Fatalf("depth: %v", err)
			}
			age := time.Duration(0)
			if depth > 0 {
				age = time.Since(oldest)
			}
			samples = append(samples, sample{
				at: time.Since(start), depth: depth, oldestAge: age,
				produced: produced.Load(), published: published.Load(),
			})
		}
	}
	sampler.Stop()
	stopProducing()
	writers.Wait()
	arrivalWindow := time.Since(start)

	// Catch-up, with the publishers still running.
	catchUpStart := time.Now()
	var remaining int64
	for time.Since(catchUpStart) < 2*time.Minute {
		var err error
		remaining, _, err = drainer.Depth(context.Background(), tenant)
		if err != nil {
			t.Fatalf("depth: %v", err)
		}
		if remaining == 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	catchUp := time.Since(catchUpStart)
	stopPublishing()
	publishers.Wait()

	total := produced.Load()
	arrivalRate := float64(total) / arrivalWindow.Seconds()
	serviceRate := float64(published.Load()) / time.Since(start).Seconds()

	t.Logf("produced %d events in %s (%.0f/s); published %d (%.0f/s); catch-up %s; depth after %d",
		total, arrivalWindow.Round(time.Second), arrivalRate,
		published.Load(), serviceRate, catchUp.Round(time.Millisecond), remaining)
	for _, s := range samples {
		t.Logf("  t=%4s depth=%7d oldest=%6s produced=%8d published=%8d",
			s.at.Round(time.Second), s.depth, s.oldestAge.Round(time.Millisecond),
			s.produced, s.published)
	}

	// The acceptance properties.

	if arrivalRate < targetRate*0.8 {
		t.Fatalf("the producer only reached %.0f events/s against a target of %d; the "+
			"measurement did not put the platform's own output through the outbox",
			arrivalRate, targetRate)
	}

	// Depth is bounded rather than trending upward. A diverging queue grows with elapsed
	// time, so the second half must not be systematically deeper than the first.
	if len(samples) < 8 {
		t.Fatalf("%d samples is too few to say anything about a trend", len(samples))
	}
	half := len(samples) / 2
	first, second := meanDepth(samples[:half]), meanDepth(samples[half:])
	t.Logf("mean depth: first half %.0f, second half %.0f", first, second)
	if second > first*2+float64(targetRate) {
		t.Errorf("the queue is deeper in the second half of the run (%.0f) than in the "+
			"first (%.0f). Depth that grows with elapsed time is a queue whose service "+
			"rate does not respond to its depth: it does not clear, it diverges.",
			second, first)
	}

	// Oldest unpublished age stays inside a bound an incident review can live with.
	const oldestBound = 30 * time.Second
	for _, s := range samples {
		if s.oldestAge > oldestBound {
			t.Errorf("at t=%s the oldest unpublished event was %s old; the analytical "+
				"plane was that far behind the period an incident review reads",
				s.at.Round(time.Second), s.oldestAge.Round(time.Second))
			break
		}
	}

	// Service keeps up with arrival over the run.
	if float64(published.Load()) < float64(total)*0.99 {
		t.Errorf("published %d of %d events; a publisher that falls behind while the "+
			"producer runs never catches up in production, where the producer does not stop",
			published.Load(), total)
	}

	// And the queue converges once arrivals stop.
	if remaining != 0 {
		t.Errorf("%d rows remain %s after the producer stopped", remaining, catchUp)
	}

	// No evidence lost: every row this run committed is still in PostgreSQL, which is the
	// record the outbox is only a delivery mechanism for.
	// Row level security is per tenant, so the count has to name one or it reads zero and
	// the assertion fails for a reason that has nothing to do with the outbox.
	if _, err := pool.Exec(context.Background(),
		`SELECT set_config('app.tenant_id', $1, false)`, tenant); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	var stored int64
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM evidence_events WHERE tenant_id = $1`, tenant).Scan(&stored); err != nil {
		t.Logf("count stored: %v", err)
	} else if stored != total {
		t.Errorf("%d of %d events are in the evidence store", stored, total)
	}
}

// sample is one observation of the queue during the run.
type sample struct {
	at        time.Duration
	depth     int64
	oldestAge time.Duration
	produced  int64
	published int64
}

func meanDepth(samples []sample) float64 {
	if len(samples) == 0 {
		return 0
	}
	var sum int64
	for _, s := range samples {
		sum += s.depth
	}
	return float64(sum) / float64(len(samples))
}

// requireNoOtherPublisher refuses to measure a database something else is draining.
//
// A canary event, left alone for a moment. If it is published by anything other than this
// test, the capacity being measured is partly somebody else's — which is how the previous
// test passed on a workstation with a gateway running and failed on one without.
func requireNoOtherPublisher(t *testing.T, ctx context.Context,
	store, drainer *evidence.Store, tenant string) {

	t.Helper()
	at := time.Now().UTC()
	canary := evidence.Event{
		SchemaVersion: evidence.SchemaVersion,
		EventID:       fmt.Sprintf("canary_%d", at.UnixNano()),
		EventName:     evidence.AuthorityEvaluated,
		TenantID:      tenant,
		AggregateID:   "canary",
		CorrelationID: "canary",
		OccurredAt:    at,
		ProducedAt:    at,
		Producer:      "assurance-gateway",
		Sequence:      1,
	}
	// AppendBatch rather than Append: only the batch path enqueues to the outbox, and a
	// canary that never reaches the queue would report every environment as contaminated.
	if err := store.AppendBatch(ctx, []evidence.Event{canary}); err != nil {
		t.Fatalf("append the canary: %v", err)
	}

	time.Sleep(3 * time.Second)

	depth, _, err := drainer.Depth(ctx, tenant)
	if err != nil {
		t.Fatalf("depth: %v", err)
	}
	if depth == 0 {
		t.Fatal("TEST_ENVIRONMENT_CONTAMINATED: something else is publishing this " +
			"database's outbox. Stop every gateway pointed at it before measuring: a " +
			"capacity test that counts another process's throughput as its own reports " +
			"a property of the workstation rather than of the build.")
	}
}

// appPool and outboxPool connect as the two roles this measurement needs: the application
// writes evidence, and the publisher — a deliberately different role — drains the outbox.
func appPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return poolFor(t, "POSTGRES_APP_DSN",
		"postgres://assurance_app:assurance_app_dev_only@localhost:5432/assurance?sslmode=disable")
}

func outboxPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return poolFor(t, "POSTGRES_OUTBOX_DSN",
		"postgres://assurance_outbox:assurance_outbox_dev_only@localhost:5432/assurance?sslmode=disable")
}

func poolFor(t *testing.T, envName, fallback string) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv(envName)
	if dsn == "" {
		dsn = fallback
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("no database for %s: %v", envName, err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("no database for %s: %v", envName, err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func streamURL(t *testing.T) string {
	t.Helper()
	if v := os.Getenv("NATS_URL"); v != "" {
		return v
	}
	return "nats://localhost:4222"
}
