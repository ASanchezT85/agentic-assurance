package execution

import (
	"context"
	"sync"
	"time"
)

// MemoryStore is an in-memory Store for tests and for the FakeBroker path.
//
// It is not the production store: ADR-015 puts idempotency truth in PostgreSQL
// precisely because an in-memory or Redis-only record cannot survive a restart, and
// a lost record is a duplicate execution waiting to happen.
type MemoryStore struct {
	mu      sync.Mutex
	records map[string]*Record

	// envelopes mirrors the unique index the PostgreSQL store relies on. Without it
	// the two stores disagree about what is allowed: every test running against this
	// one passed while reusing an envelope id under a new key, which is the one thing
	// idempotency_envelope_idx exists to refuse.
	envelopes map[string]string

	// retiredKeys and retiredEnvelopes mirror idempotency_tombstones: what the platform
	// remembers after retention has pruned an outcome. Without them this store lets a
	// spent key through and every test using it proves the opposite of production.
	retiredKeys      map[string]string
	retiredEnvelopes map[string]string

	// FailWith makes the fail-closed path testable without breaking a database.
	FailWith error
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		records: map[string]*Record{}, envelopes: map[string]string{},
		retiredKeys: map[string]string{}, retiredEnvelopes: map[string]string{},
	}
}

func key(tenantID, idempotencyKey string) string {
	return tenantID + "\x00" + idempotencyKey
}

func (m *MemoryStore) Claim(_ context.Context, rec Record) (*Record, bool, error) {
	if m.FailWith != nil {
		return nil, false, m.FailWith
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	k := key(rec.TenantID, rec.IdempotencyKey)
	if _, retired := m.retiredKeys[k]; retired {
		return nil, false, ErrKeyRetired
	}
	if rec.EnvelopeID != "" {
		if held, retired := m.retiredEnvelopes[key(rec.TenantID, rec.EnvelopeID)]; retired &&
			held != rec.IdempotencyKey {
			return nil, false, ErrEnvelopeReused
		}
	}
	if existing, ok := m.records[k]; ok {
		copied := *existing
		return &copied, false, nil
	}
	// A second key over one envelope id is a caller asking for a second order under
	// one intent, and it is refused before anything is claimed.
	if rec.EnvelopeID != "" {
		e := key(rec.TenantID, rec.EnvelopeID)
		if held, ok := m.envelopes[e]; ok && held != rec.IdempotencyKey {
			return nil, false, ErrEnvelopeReused
		}
		m.envelopes[e] = rec.IdempotencyKey
	}
	stored := rec
	m.records[k] = &stored
	return nil, true, nil
}

func (m *MemoryStore) Resolve(_ context.Context, tenantID, idempotencyKey string, o Outcome, at time.Time) error {
	if m.FailWith != nil {
		return m.FailWith
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	rec, ok := m.records[key(tenantID, idempotencyKey)]
	if !ok {
		return ErrRecordNotFound
	}
	// Replayed is a property of how an outcome was returned, not of the outcome
	// itself, so it is never persisted.
	o.Replayed = false
	rec.State = RecordResolved
	rec.Outcome = o
	rec.UpdatedAt = at.UTC()
	return nil
}

func (m *MemoryStore) Load(_ context.Context, tenantID, idempotencyKey string) (*Record, error) {
	if m.FailWith != nil {
		return nil, m.FailWith
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	rec, ok := m.records[key(tenantID, idempotencyKey)]
	if !ok {
		return nil, ErrRecordNotFound
	}
	copied := *rec
	return &copied, nil
}

// MemoryCache is an in-memory Cache standing in for Redis.
type MemoryCache struct {
	mu      sync.Mutex
	records map[string]Record

	// Disabled simulates Redis being gone. Everything must still work, slower.
	Disabled bool
}

func NewMemoryCache() *MemoryCache {
	return &MemoryCache{records: map[string]Record{}}
}

func (c *MemoryCache) Get(_ context.Context, tenantID, idempotencyKey string) (*Record, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Disabled {
		return nil, false
	}
	rec, ok := c.records[key(tenantID, idempotencyKey)]
	if !ok {
		return nil, false
	}
	return &rec, true
}

func (c *MemoryCache) Put(_ context.Context, rec Record) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Disabled {
		return
	}
	c.records[key(rec.TenantID, rec.IdempotencyKey)] = rec
}

// Flush drops every entry, standing in for a Redis restart.
func (c *MemoryCache) Flush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = map[string]Record{}
}
