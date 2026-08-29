package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"agentic-assurance/internal/intent"
)

// Sink writes telemetry to ClickHouse over its HTTP interface.
//
// HTTP with batched JSONEachRow rather than the native driver: it is ClickHouse's
// recommended high-throughput ingest path, and it keeps the core free of another
// dependency. The core does not read from here at all, which is the property that
// matters (spec section 59, ADR-021).
type Sink struct {
	BaseURL  string
	User     string
	Password string
	Database string
	Client   *http.Client
}

func NewSink(baseURL, user, password string) *Sink {
	return &Sink{
		BaseURL:  strings.TrimRight(baseURL, "/"),
		User:     user,
		Password: password,
		Database: "assurance",
		Client:   &http.Client{Timeout: 30 * time.Second},
	}
}

// intentRow is the wire shape for assurance.intents. Field names match the column
// names because JSONEachRow pairs them by name.
type intentRow struct {
	TenantID             string   `json:"tenant_id"`
	EnvelopeID           string   `json:"envelope_id"`
	CorrelationID        string   `json:"correlation_id"`
	PrincipalID          string   `json:"principal_id"`
	AccountID            string   `json:"account_id"`
	AgentID              string   `json:"agent_id"`
	InstrumentID         string   `json:"instrument_id"`
	AssetClass           string   `json:"asset_class"`
	Side                 string   `json:"side"`
	OrderType            string   `json:"order_type"`
	Notional             *float64 `json:"notional"`
	Quantity             *float64 `json:"quantity"`
	NotionalDeterminable uint8    `json:"notional_determinable"`
	StrategyID           string   `json:"strategy_id"`
	AuthorityGrantID     string   `json:"authority_grant_id"`
	AttestationLevel     string   `json:"attestation_level"`
	ModelFamily          string   `json:"model_family"`
	ModelID              string   `json:"model_id"`
	AuthorityDecision    string   `json:"authority_decision"`
	PolicyAction         string   `json:"policy_action"`
	ControlDecision      string   `json:"control_decision"`
	ControlID            string   `json:"control_id"`
	PolicyBundleID       string   `json:"policy_bundle_id"`
	ReceivedAt           string   `json:"received_at"`
}

// Decision carries what the enforcement plane concluded, so telemetry records the
// outcome alongside the request rather than requiring a join to explain itself.
type Decision struct {
	AuthorityDecision string
	PolicyAction      string
	PolicyBundleID    string

	// ControlDecision is the code of the fleet control that refused, empty when none
	// did. Recorded because "did the control work" is the question that follows every
	// control an operator authorizes, and without this the intents a THROTTLE stopped
	// look exactly like the intents a policy rule stopped.
	ControlDecision string
	ControlID       string
}

// InsertIntents writes a batch.
//
// Batching is not an optimisation here, it is the design: one HTTP request per
// intent would spend more time on round trips than on work, and the throughput
// target in spec section 50.3 is per second, not per request.
func (s *Sink) InsertIntents(ctx context.Context, envelopes []*intent.AgentExecutionEnvelope, decisions map[string]Decision) error {
	if len(envelopes) == 0 {
		return nil
	}

	var body bytes.Buffer
	enc := json.NewEncoder(&body)

	for _, e := range envelopes {
		if e == nil {
			continue
		}
		notional, determinable := intent.ClusterNotional(e.Intent)
		row := intentRow{
			TenantID:         e.TenantID,
			EnvelopeID:       e.EnvelopeID,
			CorrelationID:    e.CorrelationID,
			PrincipalID:      e.Principal.PrincipalID,
			AccountID:        e.Principal.AccountID,
			AgentID:          e.Agent.AgentID,
			InstrumentID:     e.Intent.InstrumentID,
			AssetClass:       string(e.Intent.AssetClass),
			Side:             string(e.Intent.Side),
			OrderType:        string(e.Intent.OrderType),
			Quantity:         e.Intent.Quantity,
			StrategyID:       e.Lineage.StrategyID,
			AuthorityGrantID: e.AuthorityGrantID,
			AttestationLevel: string(e.Agent.Attestation.Level),
			ModelFamily:      e.RuntimeClaims.ModelFamily.Value,
			ModelID:          e.RuntimeClaims.ModelVersion.Value,
			ReceivedAt:       e.ReceivedAt.UTC().Format("2006-01-02 15:04:05.000"),
		}
		if determinable {
			n := notional
			row.Notional = &n
			row.NotionalDeterminable = 1
		}
		if d, ok := decisions[e.EnvelopeID]; ok {
			row.AuthorityDecision = d.AuthorityDecision
			row.PolicyAction = d.PolicyAction
			row.ControlDecision = d.ControlDecision
			row.ControlID = d.ControlID
			row.PolicyBundleID = d.PolicyBundleID
		}
		if err := enc.Encode(row); err != nil {
			return fmt.Errorf("encode intent row: %w", err)
		}
	}

	return s.exec(ctx, "INSERT INTO assurance.intents FORMAT JSONEachRow", body.Bytes())
}

type dependencyRow struct {
	TenantID       string `json:"tenant_id"`
	EnvelopeID     string `json:"envelope_id"`
	AgentID        string `json:"agent_id"`
	DependencyType string `json:"dependency_type"`
	DependencyID   string `json:"dependency_id"`
	Verification   string `json:"verification"`
	ObservedAt     string `json:"observed_at"`
}

// InsertDependencies records dependency assertions exactly as declared.
//
// The verification level is stored as it arrived and is never normalised upward.
// Concentration computed over silently promoted declarations would report confidence
// nobody has (ADR-007, INV-008).
func (s *Sink) InsertDependencies(ctx context.Context, envelopes []*intent.AgentExecutionEnvelope) error {
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	rows := 0

	for _, e := range envelopes {
		if e == nil {
			continue
		}
		for _, d := range e.Dependencies {
			verification := string(d.Verification)
			if verification == "" {
				verification = string(intent.VerificationUnknown)
			}
			if err := enc.Encode(dependencyRow{
				TenantID:       e.TenantID,
				EnvelopeID:     e.EnvelopeID,
				AgentID:        e.Agent.AgentID,
				DependencyType: string(d.Type),
				DependencyID:   d.ID,
				Verification:   verification,
				ObservedAt:     d.ObservedAt.UTC().Format("2006-01-02 15:04:05.000"),
			}); err != nil {
				return fmt.Errorf("encode dependency row: %w", err)
			}
			rows++
		}
	}
	if rows == 0 {
		return nil
	}
	return s.exec(ctx, "INSERT INTO assurance.dependency_observations FORMAT JSONEachRow", body.Bytes())
}

type measurementRow struct {
	TenantID             string  `json:"tenant_id"`
	CohortID             string  `json:"cohort_id"`
	CohortPredicate      string  `json:"cohort_predicate"`
	WindowStart          string  `json:"window_start"`
	WindowEnd            string  `json:"window_end"`
	IntentCount          uint64  `json:"intent_count"`
	AuthorizedIntents    uint64  `json:"authorized_intents"`
	RefusedIntents       uint64  `json:"refused_intents"`
	AgentCount           uint64  `json:"agent_count"`
	GrossNotional        float64 `json:"gross_notional"`
	NetNotional          float64 `json:"net_notional"`
	DirectionalImbalance float64 `json:"directional_imbalance"`
	ObservedCoverage     float64 `json:"observed_coverage"`
	VerifiedCoverage     float64 `json:"verified_coverage"`
	DeclaredCoverage     float64 `json:"declared_coverage"`
	UnknownCoverage      float64 `json:"unknown_coverage"`
}

// InsertMeasurements stores computed measurements.
//
// They are stored rather than derived at query time so a historical view shows what
// was measured then, not what today's code would compute from the same rows. An
// incident review that silently recomputes with new logic is not a review of what
// happened.
func (s *Sink) InsertMeasurements(ctx context.Context, ms []Measurement) error {
	if len(ms) == 0 {
		return nil
	}
	var body bytes.Buffer
	enc := json.NewEncoder(&body)

	for _, m := range ms {
		if err := enc.Encode(measurementRow{
			TenantID:             m.TenantID,
			CohortID:             m.Cohort.ID(),
			CohortPredicate:      m.Cohort.Expression(),
			WindowStart:          m.Window.Start.UTC().Format("2006-01-02 15:04:05.000"),
			WindowEnd:            m.Window.End.UTC().Format("2006-01-02 15:04:05.000"),
			IntentCount:          uint64(m.IntentCount),
			AuthorizedIntents:    uint64(m.AuthorizedIntents),
			RefusedIntents:       uint64(m.RefusedIntents),
			AgentCount:           uint64(m.AgentCount),
			GrossNotional:        m.GrossNotional,
			NetNotional:          m.NetNotional,
			DirectionalImbalance: m.DirectionalImbalance,
			ObservedCoverage:     m.FeedCoverage.Observed,
			VerifiedCoverage:     m.FeedCoverage.Verified,
			DeclaredCoverage:     m.FeedCoverage.Declared,
			UnknownCoverage:      m.FeedCoverage.Unknown,
		}); err != nil {
			return fmt.Errorf("encode measurement row: %w", err)
		}
	}
	return s.exec(ctx, "INSERT INTO assurance.fleet_measurements FORMAT JSONEachRow", body.Bytes())
}

// InsertEvidence writes one projected evidence event.
//
// One row per call rather than a batch: the consumer already fetches in batches and
// acknowledges per message, and buffering here would mean acknowledging events that
// are not yet projected — the analytical copy silently missing what the stream says it
// delivered.
func (s *Sink) InsertEvidence(ctx context.Context, row string) error {
	return s.exec(ctx,
		"INSERT INTO assurance.evidence_stream FORMAT JSONEachRow", []byte(row+"\n"))
}

// Query runs a read-only statement and returns the raw response.
func (s *Sink) Query(ctx context.Context, sql string) (string, error) {
	out, err := s.request(ctx, sql, nil)
	return out, err
}

func (s *Sink) exec(ctx context.Context, query string, body []byte) error {
	_, err := s.request(ctx, query, body)
	return err
}

func (s *Sink) request(ctx context.Context, query string, body []byte) (string, error) {
	params := url.Values{}
	params.Set("user", s.User)
	params.Set("password", s.Password)
	params.Set("database", s.Database)
	params.Set("query", query)

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.BaseURL+"/?"+params.Encode(), reader)
	if err != nil {
		return "", err
	}

	resp, err := s.Client.Do(req)
	if err != nil {
		// Losing ClickHouse degrades analytics and nothing else (ADR-021). The
		// caller reports it and carries on; no enforcement decision waits on this.
		return "", fmt.Errorf("clickhouse unreachable: %w", err)
	}
	defer resp.Body.Close()

	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if readErr != nil {
		return "", fmt.Errorf("clickhouse read: %w", readErr)
	}
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("clickhouse returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return string(raw), nil
}
