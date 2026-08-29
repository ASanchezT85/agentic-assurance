package security

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
	"time"

	"agentic-assurance/adapters/fakebroker"
	"agentic-assurance/internal/broker"
	"agentic-assurance/internal/execution"
	"agentic-assurance/internal/intent"
)

// INV-004: no ambiguous broker timeout may trigger blind duplicate execution.
//
// This is the invariant that costs real money when it breaks. The failure is
// mundane: a submission times out, the caller has no answer, something retries, and
// one intent becomes two orders at the venue. Every guard here counts submissions
// that reached the venue rather than orders that exist, because a venue that
// deduplicates would hide the bug while the platform kept committing it.

var execAt = time.Date(2026, 8, 27, 14, 0, 0, 0, time.UTC)

func execFixture(t *testing.T) (*execution.Service, *fakebroker.Broker) {
	t.Helper()
	fake := fakebroker.New()
	fake.SetClock(func() time.Time { return execAt })
	return &execution.Service{
		Broker: fake,
		Store:  execution.NewMemoryStore(),
		Cache:  execution.NewMemoryCache(),
		Now:    func() time.Time { return execAt },
	}, fake
}

func execEnvelope(key string) *intent.AgentExecutionEnvelope {
	return &intent.AgentExecutionEnvelope{
		SchemaVersion:  intent.SchemaVersion,
		EnvelopeID:     "env_" + key,
		IdempotencyKey: key,
		TenantID:       "tenant_acme",
		ReceivedAt:     execAt,
	}
}

func execRequest(key string) broker.OrderRequest {
	return broker.OrderRequest{
		ClientOrderID: "coid_" + key,
		TenantID:      "tenant_acme",
		InstrumentID:  "instr_us_equity_00206R102",
		Symbol:        "AAPL",
		AssetClass:    intent.AssetEquity,
		Side:          intent.SideBuy,
		OrderType:     intent.OrderLimit,
		Quantity:      qty(100.0),
		LimitPrice:    ptr(50.0),
		TimeInForce:   intent.TIFDay,
	}
}

// The core case. The venue received the order and the answer was lost. However many
// times the caller retries, exactly one submission reaches the venue.
func TestAmbiguousTimeoutNeverProducesASecondOrder(t *testing.T) {
	svc, fake := execFixture(t)
	fake.InjectFault("coid_ambiguous", fakebroker.FaultTimeoutAfterReceipt)

	for i := 0; i < 10; i++ {
		if _, err := svc.Submit(context.Background(), execEnvelope("ambiguous"), execRequest("ambiguous")); err != nil {
			t.Fatalf("retry %d: %v", i, err)
		}
	}

	if n := fake.Submissions("coid_ambiguous"); n != 1 {
		t.Fatalf("%d submissions reached the venue after ten retries of one ambiguous "+
			"timeout; exactly one is permitted (INV-004)", n)
	}
}

// An unresolvable outcome stays unknown. It does not become a retry, and it does not
// become a rejection either: claiming an order failed when the platform does not
// know is the same lie in the other direction.
func TestUnresolvableOutcomeStaysUnknown(t *testing.T) {
	svc, fake := execFixture(t)
	fake.InjectFault("coid_dark", fakebroker.FaultUnreconcilable)

	got, err := svc.Submit(context.Background(), execEnvelope("dark"), execRequest("dark"))
	if !errors.Is(err, execution.ErrUnresolved) {
		t.Fatalf("error = %v, want ErrUnresolved", err)
	}
	if got.State != broker.StateUnknown {
		t.Errorf("state = %s; an unresolvable outcome is UNKNOWN, not %s", got.State, got.State)
	}
	if got.State == broker.StateRejected {
		t.Error("an unknown outcome was reported as a rejection (INV-004)")
	}

	// And retrying it still does not submit again.
	for i := 0; i < 3; i++ {
		_, _ = svc.Submit(context.Background(), execEnvelope("dark"), execRequest("dark"))
	}
	if n := fake.Submissions("coid_dark"); n != 1 {
		t.Errorf("%d submissions; an unresolved order must not be resubmitted (INV-004)", n)
	}
}

// A timeout that never reached the venue is the tempting case: nothing exists, so
// resubmitting looks safe. It is not, because "no order found" and "order not yet
// visible" are the same observation from here.
func TestTimeoutBeforeReceiptDoesNotJustifyResubmission(t *testing.T) {
	svc, fake := execFixture(t)
	fake.InjectFault("coid_lost", fakebroker.FaultTimeoutBeforeReceipt)

	got, err := svc.Submit(context.Background(), execEnvelope("lost"), execRequest("lost"))
	if !errors.Is(err, execution.ErrUnresolved) {
		t.Fatalf("error = %v, want ErrUnresolved", err)
	}
	if got.State != broker.StateUnknown {
		t.Errorf("state = %s, want UNKNOWN", got.State)
	}
	if n := fake.Submissions("coid_lost"); n != 1 {
		t.Errorf("%d submissions; a lost request is not evidence that none arrived (INV-004)", n)
	}
}

// Concurrent duplicates race on the same key. Only one may reach the venue.
func TestConcurrentDuplicatesSubmitOnce(t *testing.T) {
	svc, fake := execFixture(t)

	const callers = 16
	done := make(chan struct{}, callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			_, _ = svc.Submit(context.Background(), execEnvelope("concurrent"), execRequest("concurrent"))
		}()
	}
	for i := 0; i < callers; i++ {
		<-done
	}

	if n := fake.Submissions("coid_concurrent"); n != 1 {
		t.Fatalf("%d submissions from %d concurrent duplicates (INV-004)", n, callers)
	}
}

// The structural half: there is no retry loop to misconfigure. A submission happens
// in one place, called once, with no loop around it.
func TestSubmissionHasNoRetryLoop(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "../../internal/execution/service.go", nil, 0)
	if err != nil {
		t.Fatalf("parse service.go: %v", err)
	}

	var callSites int
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "SubmitOrder" {
			return true
		}
		callSites++

		// Walk up is not available here, so instead assert no enclosing loop by
		// checking the whole file: any for statement containing a SubmitOrder call
		// is a retry loop.
		return true
	})

	if callSites != 1 {
		t.Errorf("SubmitOrder is called from %d places in service.go; one call site is "+
			"what makes 'submitted at most once' reviewable (INV-004)", callSites)
	}

	ast.Inspect(file, func(n ast.Node) bool {
		loop, ok := n.(*ast.ForStmt)
		if !ok {
			return true
		}
		ast.Inspect(loop, func(inner ast.Node) bool {
			call, ok := inner.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "SubmitOrder" {
				t.Error("SubmitOrder is called inside a loop; that is a retry loop (INV-004)")
			}
			return true
		})
		return true
	})
}

// No exported method offers to resubmit. The absence is the guarantee, so a future
// AddRetry or ForceResubmit fails here and has to argue with the invariant.
func TestNoResubmitAPIExists(t *testing.T) {
	raw, err := os.ReadFile("../../internal/execution/service.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(raw)

	for _, banned := range []string{"func (s *Service) Resubmit", "func (s *Service) Retry",
		"MaxRetries", "RetryCount", "retryLimit"} {
		if strings.Contains(body, banned) {
			t.Errorf("service.go contains %q; there is no safe number of blind retries "+
				"for an ambiguous financial outcome (INV-004)", banned)
		}
	}
}
