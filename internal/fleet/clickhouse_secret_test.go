package fleet

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The password never appears in anything the platform can write down.
//
// Section 35: a broker or store credential is never in plaintext, never logged, never
// returned through an API, never in evidence or telemetry. It was in all of those by
// one route — the credential rode in the query string, and *url.Error prints the URL it
// failed on, so `clickhouse unreachable: Post "http://…?password=…"` went straight into
// `t.logger().Error("intent telemetry not written", "err", err)` in the gateway and into
// the fleet producer's failure log.
//
// An outage is when that path runs, so the leak was not an edge case; it was the
// ordinary behaviour of a bad afternoon.
const probePassword = "hunter2_dev_only"

func TestTheStoreCredentialIsNeverInAnErrorMessage(t *testing.T) {
	// Port 1: nothing listens, so this fails in the transport and produces the exact
	// *url.Error the loggers used to print.
	sink := NewSink("http://127.0.0.1:1", "u", probePassword)

	_, err := sink.Query(context.Background(), "SELECT 1")
	if err == nil {
		t.Fatal("a query against a closed port succeeded")
	}
	if strings.Contains(err.Error(), probePassword) {
		t.Errorf("the store password is in the error text, and three call sites log this "+
			"error verbatim:\n%s", err.Error())
	}
}

// And it is not in the URL at all, which is the property that keeps it out of a proxy's
// access log and ClickHouse's own query_log as well as out of Go's error.
func TestTheStoreCredentialTravelsInAHeaderNotTheURL(t *testing.T) {
	var gotURL, gotUser, gotKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		gotUser = r.Header.Get("X-ClickHouse-User")
		gotKey = r.Header.Get("X-ClickHouse-Key")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sink := NewSink(server.URL, "reader", probePassword)
	if _, err := sink.Query(context.Background(), "SELECT 1"); err != nil {
		t.Fatalf("query: %v", err)
	}

	if strings.Contains(gotURL, probePassword) {
		t.Errorf("the password is in the request URL: %s", gotURL)
	}
	if gotUser != "reader" || gotKey != probePassword {
		t.Errorf("the credential did not arrive in the ClickHouse headers "+
			"(user=%q, key present=%v); moving it out of the URL must not mean "+
			"failing to send it", gotUser, gotKey != "")
	}
}
