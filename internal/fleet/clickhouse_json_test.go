package fleet

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// A count reaches the console as a number.
//
// ClickHouse quotes UInt64 in JSON unless told not to, and the fleet handlers pass
// JSONEachRow through untouched, so the setting on this request is the only thing
// standing between `count()` and a console field declared `number` holding "42".
//
// Nothing downstream would report the mismatch. TypeScript believes the declaration,
// React renders a string and a number identically, and the field-name contract test
// checks that `agents` exists — not that it is a number. The one place it showed was
// the Dependencies surface picking its most-shared dependency with `>`, where "9"
// outranks "10" and the headline names the wrong dependency.
func TestCountsAreNotQuotedOnTheWire(t *testing.T) {
	var asked string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sink := NewSink(server.URL, "u", "p")
	if _, err := sink.Query(context.Background(), "SELECT count() FORMAT JSONEachRow"); err != nil {
		t.Fatalf("query: %v", err)
	}

	values, err := url.ParseQuery(asked)
	if err != nil {
		t.Fatalf("the request URL did not parse: %v", err)
	}
	if got := values.Get("output_format_json_quote_64bit_integers"); got != "0" {
		t.Errorf("output_format_json_quote_64bit_integers = %q, want \"0\"; without it "+
			"every count() and uniqExact() reaches the console as a quoted string and "+
			"the surfaces that compare them compare text", got)
	}
}
