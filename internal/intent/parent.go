package intent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"time"
)

// Signal is one piece of corroboration that a set of intents belongs to a single
// economic intent (spec section 20.2).
//
// Signals are enumerated rather than weighted. Each one either holds across the
// cluster or does not, and the reconstruction reports which, so a reader can
// disagree with the conclusion by looking at the same facts.
type Signal string

const (
	SignalSameTenant        Signal = "SAME_TENANT"
	SignalSamePrincipal     Signal = "SAME_PRINCIPAL"
	SignalSameInstrument    Signal = "SAME_INSTRUMENT"
	SignalSameSide          Signal = "SAME_SIDE"
	SignalTemporalProximity Signal = "TEMPORAL_PROXIMITY"
	SignalSameAgent         Signal = "SAME_AGENT"
	SignalSameStrategy      Signal = "SAME_STRATEGY"
	SignalSameGrant         Signal = "SAME_AUTHORITY_GRANT"
	SignalSameMarketContext Signal = "SAME_MARKET_CONTEXT"
)

// consideredSignals is the full list confidence is measured against. It is fixed so
// that confidence means the same thing between runs and between releases.
var consideredSignals = []Signal{
	SignalSameTenant, SignalSamePrincipal, SignalSameInstrument, SignalSameSide,
	SignalTemporalProximity, SignalSameAgent, SignalSameStrategy, SignalSameGrant,
	SignalSameMarketContext,
}

// ParentIntent is a reconstructed economic intent (spec section 20).
//
// It is a reconstruction, not a finding of fact. The platform observed several
// orders that look like one intention; it does not know that they were, and nothing
// here claims causality (spec section 20.2).
type ParentIntent struct {
	ParentIntentID string
	TenantID       string
	PrincipalID    string
	AccountID      string
	InstrumentID   string
	Side           Side

	ChildEnvelopeIDs []string
	ChildCount       int
	AgentIDs         []string
	AgentCount       int

	// GrossNotional is the total committed across children whose size is
	// determinable. NetNotional carries the sign of the side, because spec
	// section 23 requires both flows to survive: a cluster of buys and a cluster of
	// sells that net to zero are very different situations.
	GrossNotional float64
	NetNotional   float64

	// IndeterminateChildren counts children whose notional could not be established
	// without a market price. They are counted rather than dropped: a fragmented
	// intent hidden behind market orders is exactly the evasion this engine exists
	// to see, and reporting a gross total that silently omits them would understate
	// the exposure.
	IndeterminateChildren int

	FirstSeen time.Time
	LastSeen  time.Time
	TimeSpan  time.Duration

	// Signals lists what actually corroborated, and Confidence is the fraction of
	// considered signals that held. It is a coverage ratio with a stated
	// denominator, not a risk score with chosen weights (ADR-014).
	Signals    []Signal
	Confidence float64
}

// ClusterConfig controls reconstruction.
type ClusterConfig struct {
	// Window is the maximum gap between consecutive children of one intent. A gap
	// longer than this starts a new cluster.
	Window time.Duration

	// MinChildren is the smallest cluster worth calling a parent intent. One order
	// is an order, not a fragmented intention.
	MinChildren int
}

// DefaultClusterConfig is a starting point, not a calibrated threshold.
//
// Sixty seconds and two children are documented defaults chosen to make
// fragmentation visible in a demonstration. Spec section 60 forbids magic thresholds
// without provenance, and the provenance of these is "nobody has measured yet".
// They must be re-derived from real fragmentation patterns before anyone enforces on
// them.
var DefaultClusterConfig = ClusterConfig{
	Window:      60 * time.Second,
	MinChildren: 2,
}

// Reconstruct groups envelopes into parent intents.
//
// Deterministic clustering only (spec section 20.2): the same input produces the
// same output, always, and every grouping decision is a comparison of recorded
// fields rather than a model's opinion.
func Reconstruct(envelopes []*AgentExecutionEnvelope, cfg ClusterConfig) []ParentIntent {
	if cfg.Window <= 0 {
		cfg.Window = DefaultClusterConfig.Window
	}
	if cfg.MinChildren < 2 {
		cfg.MinChildren = DefaultClusterConfig.MinChildren
	}

	// The grouping key deliberately excludes the agent. Cross-agent accumulation
	// under one principal is the case spec section 21 is about, and keying on agent
	// would make it invisible by construction.
	type key struct {
		tenant     string
		principal  string
		account    string
		instrument string
		side       Side
	}

	buckets := map[key][]*AgentExecutionEnvelope{}
	for _, e := range envelopes {
		if e == nil {
			continue
		}
		k := key{e.TenantID, e.Principal.PrincipalID, e.Principal.AccountID,
			e.Intent.InstrumentID, e.Intent.Side}
		buckets[k] = append(buckets[k], e)
	}

	// Sorting the keys keeps the output order stable, which matters because a
	// reconstruction that reorders itself between runs is not reproducible.
	keys := make([]key, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.tenant != b.tenant {
			return a.tenant < b.tenant
		}
		if a.principal != b.principal {
			return a.principal < b.principal
		}
		if a.account != b.account {
			return a.account < b.account
		}
		if a.instrument != b.instrument {
			return a.instrument < b.instrument
		}
		return a.side < b.side
	})

	var out []ParentIntent
	for _, k := range keys {
		group := buckets[k]
		sort.SliceStable(group, func(i, j int) bool {
			if group[i].ReceivedAt.Equal(group[j].ReceivedAt) {
				return group[i].EnvelopeID < group[j].EnvelopeID
			}
			return group[i].ReceivedAt.Before(group[j].ReceivedAt)
		})

		for _, cluster := range splitByGap(group, cfg.Window) {
			if len(cluster) < cfg.MinChildren {
				continue
			}
			out = append(out, summarize(cluster))
		}
	}
	return out
}

// splitByGap breaks a time-ordered group wherever consecutive intents are further
// apart than the window.
func splitByGap(group []*AgentExecutionEnvelope, window time.Duration) [][]*AgentExecutionEnvelope {
	if len(group) == 0 {
		return nil
	}
	var clusters [][]*AgentExecutionEnvelope
	current := []*AgentExecutionEnvelope{group[0]}

	for i := 1; i < len(group); i++ {
		if group[i].ReceivedAt.Sub(group[i-1].ReceivedAt) > window {
			clusters = append(clusters, current)
			current = []*AgentExecutionEnvelope{group[i]}
			continue
		}
		current = append(current, group[i])
	}
	return append(clusters, current)
}

func summarize(cluster []*AgentExecutionEnvelope) ParentIntent {
	first, last := cluster[0], cluster[len(cluster)-1]

	p := ParentIntent{
		TenantID:     first.TenantID,
		PrincipalID:  first.Principal.PrincipalID,
		AccountID:    first.Principal.AccountID,
		InstrumentID: first.Intent.InstrumentID,
		Side:         first.Intent.Side,
		ChildCount:   len(cluster),
		FirstSeen:    first.ReceivedAt.UTC(),
		LastSeen:     last.ReceivedAt.UTC(),
	}
	p.TimeSpan = p.LastSeen.Sub(p.FirstSeen)

	agents := map[string]bool{}
	strategies := map[string]bool{}
	grants := map[string]bool{}
	markets := map[string]bool{}

	for _, e := range cluster {
		p.ChildEnvelopeIDs = append(p.ChildEnvelopeIDs, e.EnvelopeID)
		agents[e.Agent.AgentID] = true
		strategies[e.Lineage.StrategyID] = true
		grants[e.AuthorityGrantID] = true
		markets[e.Context.MarketSnapshotID] = true

		if notional, ok := ClusterNotional(e.Intent); ok {
			p.GrossNotional += notional
			if e.Intent.Side == SideSell {
				p.NetNotional -= notional
			} else {
				p.NetNotional += notional
			}
			continue
		}
		p.IndeterminateChildren++
	}

	for id := range agents {
		p.AgentIDs = append(p.AgentIDs, id)
	}
	sort.Strings(p.AgentIDs)
	p.AgentCount = len(agents)

	p.Signals = corroborating(len(agents), len(strategies), len(markets), len(grants),
		p.TimeSpan)
	p.Confidence = float64(len(p.Signals)) / float64(len(consideredSignals))
	p.ParentIntentID = parentID(p)
	return p
}

// corroborating lists which signals held across the cluster.
//
// Tenant, principal, instrument and side always hold: they are the grouping key, so
// a cluster cannot exist without them. They are still reported, because a reader
// comparing two reconstructions needs the same list from both.
func corroborating(agents, strategies, markets, grants int, span time.Duration) []Signal {
	signals := []Signal{
		SignalSameTenant, SignalSamePrincipal, SignalSameInstrument, SignalSameSide,
	}

	// Temporal proximity is a signal, not the definition. A cluster spanning the
	// whole window is weaker evidence than one spanning a second.
	if span <= DefaultClusterConfig.Window/2 {
		signals = append(signals, SignalTemporalProximity)
	}
	if agents == 1 {
		signals = append(signals, SignalSameAgent)
	}
	if strategies == 1 {
		signals = append(signals, SignalSameStrategy)
	}
	if grants == 1 {
		signals = append(signals, SignalSameGrant)
	}
	if markets == 1 {
		signals = append(signals, SignalSameMarketContext)
	}
	return signals
}

// parentID is derived from the cluster's own identity, so re-running reconstruction
// over the same intents produces the same identifier. A random id would make two
// runs of one investigation incomparable.
func parentID(p ParentIntent) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%s|%s|%s|%d|%s",
		p.TenantID, p.PrincipalID, p.AccountID, p.InstrumentID, p.Side,
		p.ChildCount, p.FirstSeen.UTC().Format(time.RFC3339Nano))
	for _, id := range p.ChildEnvelopeIDs {
		fmt.Fprintf(h, "|%s", id)
	}
	return "pi_" + hex.EncodeToString(h.Sum(nil))[:24]
}

// ClusterNotional is the notional a child contributes.
//
// It mirrors the authority and policy packages, and for the same reason: a market
// order sized by quantity has no bounded notional until it fills, and there is no
// market data on the hot path (ADR-019).
func ClusterNotional(in Intent) (float64, bool) {
	if in.Notional != nil {
		return *in.Notional, true
	}
	if in.Quantity == nil {
		return 0, false
	}
	switch in.OrderType {
	case OrderLimit, OrderStopLimit:
		if in.LimitPrice != nil {
			return *in.Quantity * *in.LimitPrice, true
		}
	}
	return 0, false
}

// Fragmented reports whether this parent intent looks like one large intention split
// to stay under a per-order ceiling (scenario S06).
//
// It reports the observation, not a verdict. Splitting an order is ordinary
// execution practice; splitting it so every piece clears a limit that the whole
// would not is the pattern worth surfacing, and a human decides what it means.
func (p ParentIntent) Fragmented(perOrderLimit float64) bool {
	if perOrderLimit <= 0 || p.ChildCount < 2 {
		return false
	}
	return p.GrossNotional > perOrderLimit
}

// CrossAgent reports whether more than one agent contributed (scenario S07).
func (p ParentIntent) CrossAgent() bool { return p.AgentCount > 1 }

// ExposureComplete reports whether the gross total accounts for every child.
//
// A caller enforcing on GrossNotional needs to know it is looking at the whole
// picture. When it is false, the real exposure is at least the reported figure and
// possibly much more.
func (p ParentIntent) ExposureComplete() bool { return p.IndeterminateChildren == 0 }
