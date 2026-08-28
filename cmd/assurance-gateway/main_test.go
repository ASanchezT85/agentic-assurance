package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"agentic-assurance/internal/evidence"
)

func TestHealthEndpoints(t *testing.T) {
	for _, path := range []string{"/healthz", "/readyz"} {
		rec := httptest.NewRecorder()
		newMux(nil, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("%s: got %d, want 200", path, rec.Code)
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if body["status"] != "ok" || body["component"] != component {
			t.Fatalf("%s: unexpected body %v", path, body)
		}
	}
}

func TestAddrDefault(t *testing.T) {
	t.Setenv("GATEWAY_ADDR", "")
	if got := addr(); got != ":8080" {
		t.Fatalf("got %q, want %q", got, ":8080")
	}
	t.Setenv("GATEWAY_ADDR", ":9999")
	if got := addr(); got != ":9999" {
		t.Fatalf("env override ignored, got %q", got)
	}
}

// stubReader stands in for the evidence store.
type stubReader struct {
	events []evidence.Event
	err    error

	gotTenant string
	gotKey    string
}

func (s *stubReader) Chain(_ context.Context, tenantID, correlationID string) ([]evidence.Event, error) {
	s.gotTenant, s.gotKey = tenantID, correlationID
	return s.events, s.err
}

func (s *stubReader) ByAggregate(_ context.Context, tenantID, aggregateID string) ([]evidence.Event, error) {
	s.gotTenant, s.gotKey = tenantID, aggregateID
	return s.events, s.err
}

func sampleEvent() evidence.Event {
	at := time.Date(2026, 8, 27, 14, 32, 4, 0, time.UTC)
	return evidence.Event{
		SchemaVersion: evidence.SchemaVersion,
		EventID:       "ev_1",
		EventName:     evidence.IntentReceived,
		TenantID:      "tenant_acme",
		AggregateID:   "env_1",
		CorrelationID: "corr_1",
		OccurredAt:    at,
		ProducedAt:    at,
		Producer:      "assurance-gateway",
	}
}

func TestEvidenceByCorrelation(t *testing.T) {
	reader := &stubReader{events: []evidence.Event{sampleEvent()}}

	req := httptest.NewRequest(http.MethodGet, "/v1/evidence?correlation_id=corr_1", nil)
	req.Header.Set("X-Tenant-Id", "tenant_acme")
	rec := httptest.NewRecorder()
	newMux(reader, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if reader.gotTenant != "tenant_acme" || reader.gotKey != "corr_1" {
		t.Errorf("queried tenant=%q key=%q", reader.gotTenant, reader.gotKey)
	}

	var body struct {
		Count  int              `json:"count"`
		Events []evidence.Event `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Count != 1 || len(body.Events) != 1 {
		t.Fatalf("body = %+v", body)
	}
	if body.Events[0].EventID != "ev_1" {
		t.Errorf("event id = %q", body.Events[0].EventID)
	}
}

func TestEvidenceByIntent(t *testing.T) {
	reader := &stubReader{events: []evidence.Event{sampleEvent()}}

	req := httptest.NewRequest(http.MethodGet, "/v1/intents/env_1/evidence", nil)
	req.Header.Set("X-Tenant-Id", "tenant_acme")
	rec := httptest.NewRecorder()
	newMux(reader, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if reader.gotKey != "env_1" {
		t.Errorf("queried aggregate %q", reader.gotKey)
	}
}

// A query with no tenant must be refused rather than served across tenants.
func TestEvidenceRequiresATenant(t *testing.T) {
	reader := &stubReader{events: []evidence.Event{sampleEvent()}}

	req := httptest.NewRequest(http.MethodGet, "/v1/evidence?correlation_id=corr_1", nil)
	rec := httptest.NewRecorder()
	newMux(reader, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d; a request with no tenant must not be served", rec.Code)
	}
	if reader.gotTenant != "" {
		t.Error("the store was queried without a tenant")
	}
}

func TestEvidenceRequiresACorrelationID(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/evidence", nil)
	req.Header.Set("X-Tenant-Id", "tenant_acme")
	rec := httptest.NewRecorder()
	newMux(&stubReader{}, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d", rec.Code)
	}
}

// With no store configured the endpoints report unavailable and the process stays
// healthy. An operator needs to be able to see the gateway is alive.
func TestEvidenceUnavailableWithoutAStore(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/evidence?correlation_id=corr_1", nil)
	req.Header.Set("X-Tenant-Id", "tenant_acme")
	rec := httptest.NewRecorder()
	newMux(nil, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", rec.Code)
	}

	health := httptest.NewRecorder()
	newMux(nil, nil).ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK {
		t.Error("the gateway reported unhealthy because a database was missing")
	}
}

// A store error must not leak its detail to the caller.
func TestStoreErrorsAreNotLeaked(t *testing.T) {
	reader := &stubReader{err: errors.New("relation \"evidence_events\" does not exist in tenant_globex")}

	req := httptest.NewRequest(http.MethodGet, "/v1/evidence?correlation_id=corr_1", nil)
	req.Header.Set("X-Tenant-Id", "tenant_acme")
	rec := httptest.NewRecorder()
	newMux(reader, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d", rec.Code)
	}
	if body := rec.Body.String(); contains(body, "tenant_globex") || contains(body, "evidence_events") {
		t.Errorf("the error response leaked internals: %s", body)
	}
}

// The HTTP surface is read-only. There is no route through which evidence can be
// written, corrected or deleted (ADR-009, INV-006).
func TestEvidenceEndpointsAreReadOnly(t *testing.T) {
	mux := newMux(&stubReader{}, nil)

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		req := httptest.NewRequest(method, "/v1/evidence?correlation_id=corr_1", nil)
		req.Header.Set("X-Tenant-Id", "tenant_acme")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code == http.StatusOK {
			t.Errorf("%s /v1/evidence was served; the evidence surface is read-only (INV-006)", method)
		}
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
