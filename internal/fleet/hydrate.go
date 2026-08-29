package fleet

import (
	"agentic-assurance/internal/money"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"agentic-assurance/internal/intent"
)

// A window of stored intents, rebuilt into the shape Measure consumes.
//
// The alternative was to compute the fleet vector in SQL. That would have produced a
// second implementation of every statistic in measure.go, and the two would have
// diverged the first time either changed: the Go one is the one with tests, and the
// SQL one is the one production would have used.
//
// So there is one implementation, and the store feeds it. What comes back is a
// PROJECTION, not the envelope that arrived. It carries exactly the fields Measure
// reads and nothing else, and TestAMeasurementSurvivesTheRoundTrip exists to fail the
// moment Measure starts reading a field the projection does not carry. Without that
// test this file is a silent way to measure the wrong thing.

// hydratedRow is one intent as ClickHouse returns it.
type hydratedRow struct {
	EnvelopeID           string  `json:"envelope_id"`
	AgentID              string  `json:"agent_id"`
	PrincipalID          string  `json:"principal_id"`
	AccountID            string  `json:"account_id"`
	InstrumentID         string  `json:"instrument_id"`
	AssetClass           string  `json:"asset_class"`
	Side                 string  `json:"side"`
	OrderType            string  `json:"order_type"`
	Notional             *string `json:"notional"`
	NotionalDeterminable string  `json:"notional_determinable"`
	StrategyID           string  `json:"strategy_id"`
	ModelFamily          string  `json:"model_family"`
	ModelID              string  `json:"model_id"`
	AuthorityDecision    string  `json:"authority_decision"`
	PolicyAction         string  `json:"policy_action"`
	// Deliberately not aliased to received_at. An alias that shadows the column it
	// derives from changes what the WHERE clause means: ClickHouse then compares the
	// formatted string against the window bounds as text, matches nothing, and
	// returns an empty result with no error. Every measurement silently became zero.
	ReceivedAt string `json:"received_at_iso"`
}

// LoadWindow rebuilds the intents of a window, with their market-data dependencies.
//
// Dependencies come from their own table and are attached by envelope id, because
// feed coverage is a per-intent statistic: an intent with no observed dependency is
// UNKNOWN coverage, not absent from the count.
func (s *Sink) LoadWindow(ctx context.Context, tenantID string, w Window) ([]Observed, error) {
	if !safeLiteral(tenantID) {
		return nil, fmt.Errorf("tenant id is not a safe literal")
	}

	from := w.Start.UTC().Format("2006-01-02 15:04:05.000")
	to := w.End.UTC().Format("2006-01-02 15:04:05.000")

	body, err := s.Query(ctx, fmt.Sprintf(`
		SELECT envelope_id, agent_id, principal_id, account_id, instrument_id,
		       asset_class, side, order_type, toString(notional) AS notional,
		       toString(notional_determinable) AS notional_determinable,
		       strategy_id, model_family, model_id,
		       authority_decision, policy_action,
		       formatDateTime(received_at, '%%Y-%%m-%%dT%%H:%%i:%%S.%%fZ') AS received_at_iso
		FROM assurance.intents
		WHERE tenant_id = '%s' AND received_at >= '%s' AND received_at < '%s'
		ORDER BY received_at
		FORMAT JSONEachRow`, tenantID, from, to))
	if err != nil {
		return nil, fmt.Errorf("read intents: %w", err)
	}

	deps, err := s.loadDependencies(ctx, tenantID, from, to)
	if err != nil {
		return nil, err
	}

	var observed []Observed
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		if line == "" {
			continue
		}
		var row hydratedRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, fmt.Errorf("decode intent row: %w", err)
		}

		received, err := time.Parse(time.RFC3339Nano, row.ReceivedAt)
		if err != nil {
			return nil, fmt.Errorf("intent %s has an unparseable received_at %q: %w",
				row.EnvelopeID, row.ReceivedAt, err)
		}

		e := &intent.AgentExecutionEnvelope{
			EnvelopeID: row.EnvelopeID,
			TenantID:   tenantID,
			ReceivedAt: received,
			Principal: intent.Principal{
				PrincipalID: row.PrincipalID,
				AccountID:   row.AccountID,
			},
			Agent:   intent.Agent{AgentID: row.AgentID},
			Lineage: intent.Lineage{StrategyID: row.StrategyID},
			Intent: intent.Intent{
				InstrumentID: row.InstrumentID,
				AssetClass:   intent.AssetClass(row.AssetClass),
				Side:         intent.Side(row.Side),
				OrderType:    intent.OrderType(row.OrderType),
			},
			Dependencies: deps[row.EnvelopeID],
		}
		if row.ModelFamily != "" {
			e.RuntimeClaims.ModelFamily = intent.Claim{Value: row.ModelFamily}
		}
		if row.ModelID != "" {
			e.RuntimeClaims.ModelVersion = intent.Claim{Value: row.ModelID}
		}

		// The notional is carried directly rather than recomputed. A stored intent
		// whose size was determinable at decision time must measure the same later,
		// and recomputing from quantity and a price we no longer have would either
		// fail or invent one.
		if row.NotionalDeterminable == "1" && row.Notional != nil {
			// Back from the analytical store into the exact type. A stored figure
			// that will not parse exactly is left absent rather than approximated:
			// this is a measurement of what was decided, and inventing a nearby
			// number would measure something that never happened.
			if n, err := money.Parse(*row.Notional); err == nil {
				e.Intent.Notional = &n
			}
		}

		// Authorized means the enforcement plane let it through to a venue. An
		// intent with no recorded decision is not assumed to have been allowed:
		// counting an unknown as authorized would overstate what reached a market,
		// which is the direction that misleads.
		observed = append(observed, Observed{
			Envelope:   e,
			Authorized: row.AuthorityDecision == "AUTHORITY_OK" && allowingAction(row.PolicyAction),
		})
	}
	return observed, nil
}

// allowingAction reports whether a policy action let an order through.
//
// OBSERVE allows and records; every other action stops the submission. Listed
// explicitly rather than as "not DENY", so a new action added later is refused by
// default instead of silently counting as flow that reached a market.
func allowingAction(action string) bool {
	return action == "ALLOW" || action == "OBSERVE"
}

func (s *Sink) loadDependencies(ctx context.Context, tenantID, from, to string) (map[string][]intent.Dependency, error) {
	body, err := s.Query(ctx, fmt.Sprintf(`
		SELECT envelope_id, dependency_type, dependency_id, verification
		FROM assurance.dependency_observations
		WHERE tenant_id = '%s' AND observed_at >= '%s' AND observed_at < '%s'
		FORMAT JSONEachRow`, tenantID, from, to))
	if err != nil {
		return nil, fmt.Errorf("read dependencies: %w", err)
	}

	out := map[string][]intent.Dependency{}
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		if line == "" {
			continue
		}
		var row struct {
			EnvelopeID     string `json:"envelope_id"`
			DependencyType string `json:"dependency_type"`
			DependencyID   string `json:"dependency_id"`
			Verification   string `json:"verification"`
		}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, fmt.Errorf("decode dependency row: %w", err)
		}
		out[row.EnvelopeID] = append(out[row.EnvelopeID], intent.Dependency{
			Type:         intent.DependencyType(row.DependencyType),
			ID:           row.DependencyID,
			Verification: intent.VerificationLevel(row.Verification),
		})
	}
	return out, nil
}

func parseFloat(s string) float64 {
	var f float64
	_, _ = fmt.Sscanf(s, "%g", &f)
	return f
}

// safeLiteral allows only characters that cannot terminate a SQL string.
//
// The analytical queries are built by concatenation because the ClickHouse HTTP
// interface takes SQL as text. That makes every interpolated value a place an
// injected quote would land, so nothing reaches one without passing here.
func safeLiteral(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_', r == '-', r == '.', r == ':':
		default:
			return false
		}
	}
	return true
}
