package gateway

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"agentic-assurance/internal/money"
)

// A grant request that would be issued as written.
func validGrantRequest() map[string]any {
	return map[string]any{
		"grant_id":              "grant_new",
		"principal_id":          "prin_test",
		"account_id":            "acct_test",
		"agent_id":              "agent_test",
		"issued_by":             "ops@example.test",
		"valid_until":           at.Add(24 * time.Hour).Format(time.RFC3339),
		"allowed_operations":    []string{"BUY"},
		"allowed_asset_classes": []string{"EQUITY"},
		"per_order_notional":    50000,
	}
}

func decodeGrant(t *testing.T, body map[string]any) grantRequest {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var req grantRequest
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return req
}

// The endpoint refuses a permission it does not enforce.
//
// margin_allowed and shorting_allowed were accepted, stored and checked by nothing. A
// customer issuing a grant with shorting_allowed=false read a control and sized their
// exposure against it, while the platform authorized the short — deciding whether a
// SELL is a short needs position data V0 does not hold.
//
// Dropping them quietly would leave that customer with the same false belief and no way
// to find out. A refusal tells them today (ADR-026).
func TestAGrantMayNotCarryAPermissionNothingEnforces(t *testing.T) {
	base := validGrantRequest()
	if problems := decodeGrant(t, base).validate(); len(problems) > 0 {
		t.Fatalf("the fixture is not a valid grant: %v", problems)
	}

	for _, field := range []string{"margin_allowed", "shorting_allowed"} {
		for _, value := range []bool{true, false} {
			body := validGrantRequest()
			body[field] = value

			problems := decodeGrant(t, body).validate()
			found := false
			for _, p := range problems {
				if strings.Contains(p, field) {
					found = true
				}
			}
			if !found {
				// false is refused as firmly as true. A customer who writes
				// shorting_allowed=false is asking for a denial, and accepting the
				// request is promising them one.
				t.Errorf("%s: %v was accepted; it is enforced by nothing (ADR-026). "+
					"Problems: %v", field, value, problems)
			}
		}
	}
}

// The ordinary refusals the same function makes, so the capability check cannot be the
// only reason a grant is ever rejected.
func TestAGrantWithoutACeilingIsRefused(t *testing.T) {
	body := validGrantRequest()
	body["per_order_notional"] = money.Amount(0)

	if problems := decodeGrant(t, body).validate(); len(problems) == 0 {
		t.Error("a grant with no per-order ceiling was accepted; that is an absent " +
			"ceiling rather than a generous one")
	}
}
