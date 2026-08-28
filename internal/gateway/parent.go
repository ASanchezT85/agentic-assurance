package gateway

import (
	"sync"
	"time"

	"agentic-assurance/internal/intent"
)

// ParentTracker keeps a bounded window of recent envelopes per tenant so step 7 of
// the hot path can reconstruct a fragmented economic intent.
//
// In memory, per process, and bounded. That is a real limitation with a real
// consequence: with more than one gateway replica, children that land on different
// replicas are not clustered together, so the reconstruction understates. It is
// stated rather than hidden because the engine already reports Confidence and
// ExposureComplete, and an understated cluster is readable as such.
//
// It informs; it never denies. Spec section 20 makes the reconstruction a hypothesis
// with a confidence, and denying on a hypothesis would be enforcing a guess.
type ParentTracker struct {
	Config  intent.ClusterConfig
	Window  time.Duration
	MaxKeep int

	mu     sync.Mutex
	recent map[string][]*intent.AgentExecutionEnvelope
}

func NewParentTracker(cfg intent.ClusterConfig) *ParentTracker {
	return &ParentTracker{
		Config:  cfg,
		Window:  10 * time.Minute,
		MaxKeep: 500,
		recent:  map[string][]*intent.AgentExecutionEnvelope{},
	}
}

// Observe records an envelope and returns the parent intent it now belongs to, if
// the cluster is large enough to be one.
func (t *ParentTracker) Observe(env *intent.AgentExecutionEnvelope) (intent.ParentIntent, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	kept := append(t.prune(t.recent[env.TenantID], env.ReceivedAt), env)
	if len(kept) > t.MaxKeep {
		kept = kept[len(kept)-t.MaxKeep:]
	}
	t.recent[env.TenantID] = kept

	for _, p := range intent.Reconstruct(kept, t.Config) {
		for _, id := range p.ChildEnvelopeIDs {
			if id == env.EnvelopeID {
				return p, true
			}
		}
	}
	return intent.ParentIntent{}, false
}

// prune drops envelopes older than the window. Without it the tracker grows without
// bound and clusters things minutes apart that have nothing to do with each other.
func (t *ParentTracker) prune(envs []*intent.AgentExecutionEnvelope, now time.Time) []*intent.AgentExecutionEnvelope {
	cutoff := now.Add(-t.Window)
	keep := envs[:0:0]
	for _, e := range envs {
		if e.ReceivedAt.After(cutoff) {
			keep = append(keep, e)
		}
	}
	return keep
}
