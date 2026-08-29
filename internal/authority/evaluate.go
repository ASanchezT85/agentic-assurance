package authority

import (
	"context"
	"fmt"
	"time"

	"agentic-assurance/internal/intent"
	"agentic-assurance/internal/money"
)

// Decision is the outcome of authority evaluation. Authority produces ALLOW or DENY
// and nothing else: a hard identity or authority failure is a denial, not a
// recommendation (spec section 16).
type Decision struct {
	Allowed     bool
	Code        string
	Reason      string
	GrantID     string
	EvaluatedAt time.Time
}

const codeAllowed = "AUTHORITY_OK"

func allow(g *Grant, now time.Time) Decision {
	return Decision{Allowed: true, Code: codeAllowed, GrantID: g.GrantID, EvaluatedAt: now}
}

func deny(g *Grant, now time.Time, code, reason string) Decision {
	d := Decision{Allowed: false, Code: code, Reason: reason, EvaluatedAt: now}
	if g != nil {
		d.GrantID = g.GrantID
	}
	return d
}

// Snapshot is the usage already consumed under a grant. It is a snapshot rather than
// a live query per limit so that one evaluation sees one consistent view.
type Snapshot struct {
	Rolling1hNotional money.Amount
	DailyNotional     money.Amount
	OpenOrders        int
}

// UsageSource supplies consumed usage. It is a port: the hot path uses the
// PostgreSQL-backed implementation, tests use an in-memory one.
type UsageSource interface {
	Usage(ctx context.Context, tenantID, grantID string, now time.Time) (Snapshot, error)
}

// Evaluate decides whether an envelope is within its grant.
//
// The order of checks is deliberate. Identity and lifecycle failures are cheaper and
// more serious than limit arithmetic, and a revoked grant must never produce a
// "limit exceeded" message that implies it would otherwise have been fine.
func Evaluate(ctx context.Context, env *intent.AgentExecutionEnvelope, g *Grant, usage UsageSource, now time.Time) Decision {
	now = now.UTC()

	if g == nil {
		return deny(nil, now, "GRANT_NOT_FOUND",
			"the envelope references an authority grant that does not exist")
	}
	if env == nil {
		return deny(g, now, "ENVELOPE_ABSENT", "no envelope to evaluate")
	}

	// Tenant first. A grant from another tenant is not a wrong grant, it is a
	// cross-tenant read that should never have been possible (INV-007).
	if g.TenantID != env.TenantID {
		return deny(g, now, "GRANT_WRONG_TENANT",
			"grant belongs to a different tenant")
	}
	if g.GrantID != env.AuthorityGrantID {
		return deny(g, now, "GRANT_MISMATCH",
			"the grant supplied is not the one the envelope references")
	}

	switch {
	case g.Status == StatusRevoked:
		return deny(g, now, "GRANT_REVOKED", "grant was revoked at "+revokedAtString(g))
	case now.Before(g.ValidFrom):
		return deny(g, now, "GRANT_NOT_YET_VALID",
			"grant is not valid until "+g.ValidFrom.UTC().Format(time.RFC3339))
	case !now.Before(g.ValidUntil):
		return deny(g, now, "GRANT_EXPIRED",
			"grant expired at "+g.ValidUntil.UTC().Format(time.RFC3339))
	}

	if g.AgentID != env.Agent.AgentID {
		return deny(g, now, "GRANT_WRONG_AGENT", "grant was issued to a different agent")
	}
	if g.PrincipalID != env.Principal.PrincipalID {
		return deny(g, now, "GRANT_WRONG_PRINCIPAL", "grant was issued for a different principal")
	}
	if g.AccountID != env.Principal.AccountID {
		return deny(g, now, "GRANT_WRONG_ACCOUNT", "grant was issued for a different account")
	}

	if !g.allowsSide(env.Intent.Side) {
		return deny(g, now, "OPERATION_NOT_ALLOWED",
			string(env.Intent.Side)+" is not in the grant's allowed operations")
	}
	if !g.allowsAssetClass(env.Intent.AssetClass) {
		return deny(g, now, "ASSET_CLASS_NOT_ALLOWED",
			string(env.Intent.AssetClass)+" is not in the grant's allowed asset classes")
	}
	if !g.allowsInstrument(env.Intent.InstrumentID) {
		return deny(g, now, "INSTRUMENT_NOT_ALLOWED",
			"instrument is denied or outside the grant's allow-list")
	}

	return evaluateLimits(ctx, env, g, usage, now)
}

func revokedAtString(g *Grant) string {
	if g.RevokedAt == nil {
		return "an unrecorded time"
	}
	return g.RevokedAt.UTC().Format(time.RFC3339)
}

// EffectiveNotional returns the notional this intent commits, and whether that is
// determinable without a market price.
//
// A market order sized by quantity has no bounded notional until it fills, and the
// platform has no market data on the hot path by design (ADR-019). A limit price
// bounds the exposure, so LIMIT and STOP_LIMIT are determinable; a stop order is
// not, because it becomes a market order once triggered and the stop price is a
// trigger, not a fill price.
func EffectiveNotional(in intent.Intent) (money.Amount, bool) {
	if in.Notional != nil {
		amount, err := money.FromFloat(*in.Notional)
		if err != nil {
			// More precision than the platform keeps. Refusing is the only honest
			// answer: rounding here would authorize an amount the caller did not ask
			// for, and the caller is the only one who can say which they meant.
			return 0, false
		}
		return amount, true
	}
	if in.Quantity == nil {
		return 0, false
	}
	switch in.OrderType {
	case intent.OrderLimit, intent.OrderStopLimit:
		if in.LimitPrice != nil {
			price, err := money.FromFloat(*in.LimitPrice)
			if err != nil {
				return 0, false
			}
			return money.Notional(price, *in.Quantity), true
		}
	}
	return 0, false
}

func evaluateLimits(ctx context.Context, env *intent.AgentExecutionEnvelope, g *Grant, usage UsageSource, now time.Time) Decision {
	limits := g.Limits
	needsNotional := limits.PerOrderNotional > 0 || limits.Rolling1hNotional > 0 || limits.DailyNotional > 0
	needsUsage := limits.Rolling1hNotional > 0 || limits.DailyNotional > 0 || limits.MaxOpenOrders > 0

	notional, determinable := EffectiveNotional(env.Intent)
	if needsNotional && !determinable {
		// Fail closed. Waving through an order whose size cannot be established
		// against a grant that caps size is the same as having no cap.
		return deny(g, now, "NOTIONAL_INDETERMINATE",
			"the grant caps notional, but this order's notional cannot be determined without a market price")
	}

	if limits.PerOrderNotional > 0 && notional > limits.PerOrderNotional {
		return deny(g, now, "PER_ORDER_LIMIT_EXCEEDED",
			fmt.Sprintf("order notional %s exceeds the per-order limit %s",
				notional, limits.PerOrderNotional))
	}

	if !needsUsage {
		return allow(g, now)
	}

	if usage == nil {
		return deny(g, now, "USAGE_UNAVAILABLE",
			"the grant has rolling limits but no usage source is configured")
	}
	consumed, err := usage.Usage(ctx, g.TenantID, g.GrantID, now)
	if err != nil {
		// Spec section 17: hard policy unavailable -> DENY. A rolling limit that
		// cannot be read is a limit that cannot be enforced.
		return deny(g, now, "USAGE_UNAVAILABLE",
			"consumed usage could not be read: "+err.Error())
	}

	if code, reason := checkLimits(limits, consumed, notional); code != "" {
		return deny(g, now, code, reason)
	}

	return allow(g, now)
}
