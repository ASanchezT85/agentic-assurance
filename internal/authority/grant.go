// Package authority decides what an agent is allowed to do.
//
// An AuthorityGrant is the only thing that confers permission. A natural-language
// prompt is never enforceable authority (spec section 14.1): if it is not in the
// grant, the agent does not have it.
package authority

import (
	"agentic-assurance/internal/money"

	"time"

	"agentic-assurance/internal/intent"
)

// Status is the grant lifecycle. V0 has exactly two states plus the implicit ones
// the validity window produces. There is no PAUSED, because a paused grant that
// something forgets to resume is indistinguishable from a revoked one that something
// forgot to revoke.
type Status string

const (
	StatusActive  Status = "ACTIVE"
	StatusRevoked Status = "REVOKED"
)

// Limits are notional ceilings plus a concurrency ceiling. All are optional: a zero
// value means the limit is not configured, not that the limit is zero.
//
// This is the one place where zero-means-unset is acceptable, because a grant with a
// zero ceiling would permit nothing and is better expressed by revoking it.
// Limits are exact.
//
// They were float64 while the database stored grant limits at scale 4 and consumed
// usage at scale 2, so the number a ceiling was evaluated against was not necessarily
// the number later counted against it. A ceiling that is approximately enforced is not
// a ceiling.
type Limits struct {
	PerOrderNotional  money.Amount `json:"per_order_notional"`
	Rolling1hNotional money.Amount `json:"rolling_1h_notional"`
	DailyNotional     money.Amount `json:"daily_notional"`
	MaxOpenOrders     int          `json:"max_open_orders"`
}

// Capabilities are permissions V0 records but does not yet enforce, because
// enforcing them needs position data the platform does not have until Phase 5.
// They are carried rather than dropped so the grant round-trips faithfully, and
// EnforcedCapabilities documents exactly which ones are live.
type Capabilities struct {
	MarginAllowed   bool `json:"margin_allowed"`
	ShortingAllowed bool `json:"shorting_allowed"`
}

// Grant is the authority record (spec section 14.2).
type Grant struct {
	GrantID     string `json:"grant_id"`
	TenantID    string `json:"tenant_id"`
	PrincipalID string `json:"principal_id"`
	AccountID   string `json:"account_id"`
	AgentID     string `json:"agent_id"`

	IssuedAt   time.Time `json:"issued_at"`
	ValidFrom  time.Time `json:"valid_from"`
	ValidUntil time.Time `json:"valid_until"`

	AllowedOperations   []intent.Side       `json:"allowed_operations"`
	AllowedAssetClasses []intent.AssetClass `json:"allowed_asset_classes"`

	// AllowedInstruments empty means every instrument the asset-class rule permits.
	// DeniedInstruments always wins over AllowedInstruments.
	AllowedInstruments []string `json:"allowed_instruments"`
	DeniedInstruments  []string `json:"denied_instruments"`

	Limits       Limits       `json:"limits"`
	Capabilities Capabilities `json:"capabilities"`

	Status    Status     `json:"status"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
	// RevocationReason is kept because a revocation without a reason is an
	// operational mystery six months later (spec section 36: humans are audited too).
	RevocationReason string `json:"revocation_reason,omitempty"`
}

// EnforcedCapabilities lists what is actually checked today. It exists so the gap
// between "recorded" and "enforced" is a fact in the code rather than folklore.
func EnforcedCapabilities() []string { return nil }

// Revoke marks a grant revoked. Revocation is not deletion: the record stays, so
// decisions taken under it remain explainable (ADR-009).
func (g *Grant) Revoke(at time.Time, reason string) {
	g.Status = StatusRevoked
	t := at.UTC()
	g.RevokedAt = &t
	g.RevocationReason = reason
}

// Active reports whether the grant confers any authority at the given instant.
func (g *Grant) Active(now time.Time) bool {
	return g.Status == StatusActive &&
		!now.Before(g.ValidFrom) &&
		now.Before(g.ValidUntil)
}

func (g *Grant) allowsSide(s intent.Side) bool {
	// An empty allow-list denies everything. An operation nobody wrote down is an
	// operation nobody authorized.
	for _, allowed := range g.AllowedOperations {
		if allowed == s {
			return true
		}
	}
	return false
}

func (g *Grant) allowsAssetClass(a intent.AssetClass) bool {
	for _, allowed := range g.AllowedAssetClasses {
		if allowed == a {
			return true
		}
	}
	return false
}

func (g *Grant) allowsInstrument(instrumentID string) bool {
	for _, denied := range g.DeniedInstruments {
		if denied == instrumentID {
			return false
		}
	}
	if len(g.AllowedInstruments) == 0 {
		return true
	}
	for _, allowed := range g.AllowedInstruments {
		if allowed == instrumentID {
			return true
		}
	}
	return false
}
