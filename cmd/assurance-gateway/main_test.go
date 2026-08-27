package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthEndpoints(t *testing.T) {
	for _, path := range []string{"/healthz", "/readyz"} {
		rec := httptest.NewRecorder()
		newMux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

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
