package security

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
	"time"

	"agentic-assurance/adapters/fakebroker"
	"agentic-assurance/internal/execution"
)

// INV-011: Redis loss cannot destroy authoritative financial-control state.
//
// This is the invariant ADR-015 was written to make true. Spec section 17 requires a
// duplicate to return the prior outcome, section 33.3 put the idempotency cache in
// Redis, and a prior outcome that lives only in Redis disappears on restart. The
// resolution: PostgreSQL holds the truth, Redis holds a copy, and losing the copy
// costs latency.

var errStoreDown = errors.New("idempotency store unavailable")

// Flushing the cache mid-flight must not turn a duplicate into a new order.
func TestCacheLossDoesNotCreateADuplicate(t *testing.T) {
	fake := fakebroker.New()
	fake.SetClock(func() time.Time { return execAt })
	cache := execution.NewMemoryCache()
	svc := &execution.Service{
		Broker: fake,
		Store:  execution.NewMemoryStore(),
		Cache:  cache,
		Now:    func() time.Time { return execAt },
	}

	first, err := svc.Submit(context.Background(), execEnvelope("flush"), execRequest("flush"))
	if err != nil {
		t.Fatalf("first submit: %v", err)
	}

	// Redis restarts. Everything it held is gone.
	cache.Flush()

	second, err := svc.Submit(context.Background(), execEnvelope("flush"), execRequest("flush"))
	if err != nil {
		t.Fatalf("after cache loss: %v", err)
	}

	if n := fake.Submissions("coid_flush"); n != 1 {
		t.Fatalf("%d submissions after the cache was flushed; losing Redis must not "+
			"destroy the record of a prior execution (INV-011)", n)
	}
	if !second.Replayed {
		t.Error("the duplicate was not recognised from the store after cache loss")
	}
	if second.BrokerOrderID != first.BrokerOrderID {
		t.Errorf("the replayed outcome differs from the original: %q vs %q",
			second.BrokerOrderID, first.BrokerOrderID)
	}
}

// Redis being entirely unavailable is the same story with the cache never answering.
func TestCacheUnavailableChangesNothingButLatency(t *testing.T) {
	fake := fakebroker.New()
	fake.SetClock(func() time.Time { return execAt })
	cache := execution.NewMemoryCache()
	cache.Disabled = true

	svc := &execution.Service{
		Broker: fake,
		Store:  execution.NewMemoryStore(),
		Cache:  cache,
		Now:    func() time.Time { return execAt },
	}

	for i := 0; i < 5; i++ {
		if _, err := svc.Submit(context.Background(), execEnvelope("nocache"), execRequest("nocache")); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if n := fake.Submissions("coid_nocache"); n != 1 {
		t.Fatalf("%d submissions with the cache unavailable (INV-011)", n)
	}
}

// A service with no cache at all behaves identically.
func TestNoCacheConfiguredBehavesIdentically(t *testing.T) {
	fake := fakebroker.New()
	fake.SetClock(func() time.Time { return execAt })
	svc := &execution.Service{
		Broker: fake,
		Store:  execution.NewMemoryStore(),
		Cache:  nil,
		Now:    func() time.Time { return execAt },
	}

	first, err := svc.Submit(context.Background(), execEnvelope("nilcache"), execRequest("nilcache"))
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := svc.Submit(context.Background(), execEnvelope("nilcache"), execRequest("nilcache"))
	if err != nil {
		t.Fatalf("second: %v", err)
	}

	if n := fake.Submissions("coid_nilcache"); n != 1 {
		t.Fatalf("%d submissions with no cache configured (INV-011)", n)
	}
	if !second.Replayed || second.State != first.State {
		t.Error("the duplicate was not served correctly without a cache")
	}
}

// The store fails closed. An unavailable authoritative store stops the submission
// rather than letting it proceed unrecorded, which is what would leave an order at
// the venue that the platform has no record of.
func TestUnavailableStoreStopsTheSubmission(t *testing.T) {
	fake := fakebroker.New()
	store := execution.NewMemoryStore()
	store.FailWith = errStoreDown

	svc := &execution.Service{
		Broker: fake,
		Store:  store,
		Cache:  execution.NewMemoryCache(),
		Now:    func() time.Time { return execAt },
	}

	if _, err := svc.Submit(context.Background(), execEnvelope("storedown"), execRequest("storedown")); err == nil {
		t.Fatal("a submission proceeded with the authoritative store unavailable (INV-011)")
	}
	if n := fake.Submissions("coid_storedown"); n != 0 {
		t.Fatalf("%d submissions reached the venue with no idempotency record written", n)
	}
}

// The Cache interface must stay strictly smaller than Store. A cache that could
// claim a key would be deciding whether a submission is new, which is the authority
// this invariant reserves for PostgreSQL.
func TestCacheInterfaceCannotClaimAKey(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "../../internal/execution/idempotency.go", nil, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	var cacheMethods []string
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.TypeSpec)
		if !ok || spec.Name.Name != "Cache" {
			return true
		}
		iface, ok := spec.Type.(*ast.InterfaceType)
		if !ok {
			return false
		}
		for _, m := range iface.Methods.List {
			for _, name := range m.Names {
				cacheMethods = append(cacheMethods, name.Name)
			}
		}
		return false
	})

	if len(cacheMethods) == 0 {
		t.Fatal("the Cache interface was not found; this guard is no longer checking anything")
	}
	for _, m := range cacheMethods {
		if m == "Claim" || m == "Resolve" {
			t.Errorf("the Cache interface exposes %s; a cache must not be able to decide "+
				"that a submission is new (ADR-015, INV-011)", m)
		}
	}
}
