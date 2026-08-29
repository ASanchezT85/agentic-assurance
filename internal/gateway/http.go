package gateway

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"agentic-assurance/internal/authority"
	"agentic-assurance/internal/evidence"
	"agentic-assurance/internal/identity"
	"agentic-assurance/internal/policy"
)

// MaxEnvelopeBytes bounds a submission body. An unbounded read is a denial of
// service against the enforcement plane, and an envelope is a small document.
const MaxEnvelopeBytes = 256 << 10

// presentedFrom adapts an HTTP request to what identity understands.
//
// The adaptation lives here rather than in internal/identity because that package is
// on the enforcement path and INV-005 forbids it from importing net/http.
func presentedFrom(r *http.Request, creds *identity.Credentials) identity.Presented {
	var certs []*x509.Certificate
	if r.TLS != nil {
		certs = r.TLS.PeerCertificates
	}
	return identity.FromTransport(r.Header.Get("Authorization"), certs, creds)
}

// callerTenant establishes which tenant a caller speaks for, or the status and message
// to answer with.
//
// Shared by every gateway handler that carries tenant data, so a new endpoint cannot
// quietly get a different answer to the same question.
func callerTenant(r *http.Request, creds *identity.Credentials,
	verifier *identity.Verifier) (tenant string, status int, message string) {

	if verifier == nil {
		verifier = &identity.Verifier{}
	}
	attested := verifier.Resolve(presentedFrom(r, creds))

	if err := identity.RequireExecutable(attested); err != nil {
		return "", http.StatusUnauthorized, "the caller is not authenticated"
	}
	if attested.TenantID == "" {
		return "", http.StatusUnauthorized,
			"the caller is authenticated but no tenant is established for it"
	}
	if claimed := strings.TrimSpace(r.Header.Get("X-Tenant-Id")); claimed != "" &&
		claimed != attested.TenantID {
		return "", http.StatusForbidden,
			"the request names a tenant this caller is not authenticated for"
	}
	return attested.TenantID, 0, ""
}

// SubmitHandler is POST /v1/intents.
func SubmitHandler(p *Pipeline, creds *identity.Credentials) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, MaxEnvelopeBytes+1))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody("the request body could not be read"))
			return
		}
		if len(body) > MaxEnvelopeBytes {
			writeJSON(w, http.StatusRequestEntityTooLarge,
				errorBody("the envelope exceeds the maximum accepted size"))
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		result := p.Submit(ctx, body, presentedFrom(r, creds))
		writeJSON(w, statusFor(result), submitBody(result))
	}
}

// statusFor maps a decision onto HTTP.
//
// A denial is 403 and not 200-with-a-field, because a client that ignores the body
// must not read a refusal as an acceptance. An unresolved outcome is 202: the
// platform accepted the intent and does not yet know what the venue did, which is
// neither success nor failure (INV-004).
func statusFor(r Result) int {
	switch {
	case r.Accepted:
		return http.StatusOK
	case r.Code == "OUTCOME_UNKNOWN":
		return http.StatusAccepted
	case r.Stage == StageValidation:
		return http.StatusBadRequest
	case r.Stage == StageIdentity:
		return http.StatusUnauthorized
	case r.Stage == StageExecution:
		return http.StatusUnprocessableEntity
	default:
		return http.StatusForbidden
	}
}

func submitBody(r Result) map[string]any {
	body := map[string]any{
		"accepted":       r.Accepted,
		"stage":          r.Stage,
		"code":           r.Code,
		"reason":         r.Reason,
		"envelope_id":    r.EnvelopeID,
		"correlation_id": r.CorrelationID,
		"replayed":       r.Replayed,
		"decided_at":     r.DecidedAt,
	}
	if len(r.Details) > 0 {
		body["details"] = r.Details
	}
	if r.Attested.Level != "" {
		body["attestation_level"] = string(r.Attested.Level)
	}
	if r.Policy != nil {
		// The bundle that produced the decision travels with it. A decision without
		// its bundle is an assertion that some policy, once, said no (ADR-010).
		body["policy"] = map[string]any{
			"action":        string(r.Policy.Action),
			"bundle_id":     r.Policy.BundleID,
			"bundle_hash":   r.Policy.ContentHash,
			"decided_by":    r.Policy.DecidedBy,
			"matched_rules": r.Policy.MatchedRules,
		}
	}
	if r.Authority != nil {
		body["authority"] = map[string]any{
			"allowed":  r.Authority.Allowed,
			"grant_id": r.Authority.GrantID,
			"code":     r.Authority.Code,
		}
	}
	if r.Outcome != nil {
		body["order"] = map[string]any{
			"state":           string(r.Outcome.State),
			"client_order_id": r.Outcome.ClientOrderID,
			"broker_order_id": r.Outcome.BrokerOrderID,
			"filled_quantity": r.Outcome.FilledQuantity,
			"reject_reason":   r.Outcome.RejectReason,
		}
	}
	return body
}

func errorBody(message string) map[string]any { return map[string]any{"error": message} }

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// StoreGrants adapts authority.Store to the pipeline's provider.
type StoreGrants struct{ Store *authority.Store }

func (g StoreGrants) Load(ctx context.Context, tenantID, grantID string) (*authority.Grant, error) {
	if g.Store == nil {
		return nil, errors.New("no authority store is configured")
	}
	grant, err := g.Store.Load(ctx, tenantID, grantID)
	if err != nil {
		return nil, err
	}
	if grant == nil {
		return nil, fmt.Errorf("no such authority grant")
	}
	return grant, nil
}

// FileBundles serves customer-authorized policy bundles from a directory, one JSON file
// per tenant plus one authorization file beside it.
//
// Two signatures, over two different facts.
//
// The bundle's signature covers its rules and deliberately excludes its activation
// block, because policy content must keep a stable identity across promotion. That left
// promotion unauthorized: anyone who could edit the file could take a correctly signed
// SHADOW bundle, change one word to ACTIVE, and put it into force without the customer's
// key. The signature still verified, because the rules had not changed.
//
// So a second signed document says the customer authorized *this* bundle, named by
// content hash, to enforce. Content identity and activation authority are different facts
// and are signed separately. The gateway verifies both and activates neither: a platform
// that authorizes its own policy is deciding what constrains it (INV-009, ADR-010).
type FileBundles struct {
	Dir       string
	PublicKey ed25519.PublicKey

	// Activations holds the keys that may authorize an activation and the transitions
	// that were accepted.
	//
	// Nil means no bundle can be activated at all. That is deliberate and it is the
	// fail-safe direction: without it the gateway cannot tell an authorized promotion
	// from an edited file, and enforcing a policy it cannot attribute is worse than
	// refusing to change the one already in force.
	Activations *policy.ActivationStore

	Now func() time.Time

	// Report is how a refused reload becomes visible.
	//
	// A candidate that does not verify leaves the previous policy in force and returns
	// no error, because failing live submissions over a badly staged file would be a
	// worse outcome than continuing to enforce what the customer last authorized. That
	// makes the refusal silent, and a silent refusal is how an operator discovers three
	// days later that the policy they thought they shipped never took effect.
	//
	// A callback rather than a logger, because INV-013 keeps a logger out of packages
	// that handle evidence: the binary logs, this reports.
	Report func(tenantID string, err error)

	mu     sync.RWMutex
	cached map[string]*policy.Bundle

	// reload serializes the slow path.
	//
	// Under load, a thousand concurrent submissions all saw the same changed stamp and
	// all tried to accept the same activation. One won and the rest were refused as
	// replays — of their own authorization — so a correctly staged policy produced
	// POLICY_UNAVAILABLE for a fifth of the traffic. The fast path still takes only a
	// read lock; this is held while a file is verified, which happens a few times a
	// year rather than a few thousand times a second.
	reload sync.Mutex

	// stamps is the file each cached bundle was read from, as size and modification
	// time. It is what makes activation and rollback take effect: the bundle used to
	// be read once and kept forever, so replacing the active file — or rolling back to
	// the previous one during an incident — did nothing until somebody restarted the
	// gateway. An activation that needs a restart is not an activation.
	stamps map[string]string
}

func NewFileBundles(dir, publicKeyHex string) (*FileBundles, error) {
	key, err := hex.DecodeString(strings.TrimSpace(publicKeyHex))
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("POLICY_PUBLIC_KEY must be a hex-encoded ed25519 public key")
	}
	return &FileBundles{Dir: dir, PublicKey: key,
		cached: map[string]*policy.Bundle{}, stamps: map[string]string{}}, nil
}

// Active returns the tenant's enforcing bundle, re-reading the files when they change.
//
// The check is one stat per submission rather than a re-read: verification happens when
// a file changes, and unchanged files return the cached bundle exactly as before.
//
// A bundle becomes enforceable only when all of these hold: its own signature verifies,
// it belongs to this tenant, it is ACTIVE, a customer authorization names it by content
// hash and verifies against a registered activation key that is neither revoked nor
// expired, that authorization has not been seen before, and the transition committed to
// PostgreSQL together with its evidence. Any of those failing leaves whatever was already
// in force exactly as it was.
func (f *FileBundles) Active(ctx context.Context, tenantID string) (*policy.Bundle, error) {
	// The tenant id is a path element here, so it is checked rather than trusted.
	// Traversal and separators are refused; an ordinary dot is not, because a tenant
	// id shaped like a domain is plausible and would otherwise become a silent
	// POLICY_UNAVAILABLE denial that no operator could explain.
	if tenantID == "" || tenantID == "." || tenantID == ".." ||
		strings.ContainsAny(tenantID, `/\`) || strings.Contains(tenantID, "..") {
		return nil, fmt.Errorf("invalid tenant id")
	}

	path := filepath.Join(f.Dir, tenantID+".json")
	authPath := filepath.Join(f.Dir, tenantID+".activation.json")
	stamp := bundleStamp(path) + "|" + bundleStamp(authPath)

	f.mu.RLock()
	cached, isCached := f.cached[tenantID]
	cachedStamp := f.stamps[tenantID]
	f.mu.RUnlock()

	if isCached && stamp == cachedStamp {
		return cached, nil
	}

	f.reload.Lock()
	defer f.reload.Unlock()

	// Re-read under the lock. Whoever held it may have done this exact reload while
	// this goroutine waited, and repeating it would present an authorization that has
	// just been accepted.
	f.mu.RLock()
	cached, isCached = f.cached[tenantID]
	cachedStamp = f.stamps[tenantID]
	f.mu.RUnlock()
	if isCached && stamp == cachedStamp {
		return cached, nil
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		if isCached {
			// The file went away or cannot be read. What was verified and authorized
			// stays in force: a policy that vanishes must not become no policy, and
			// dropping enforcement because a file is missing is the loudest possible
			// way to fail open.
			return cached, nil
		}
		return nil, fmt.Errorf("no policy bundle for this tenant: %w", err)
	}

	var bundle policy.Bundle
	if err := json.Unmarshal(raw, &bundle); err != nil {
		return f.keepOnFailedReload(tenantID, isCached, cached,
			fmt.Errorf("policy bundle is unreadable: %w", err))
	}
	if err := bundle.VerifySignature(f.PublicKey); err != nil {
		return f.keepOnFailedReload(tenantID, isCached, cached,
			fmt.Errorf("policy bundle signature is invalid: %w", err))
	}
	if bundle.TenantID != tenantID {
		return f.keepOnFailedReload(tenantID, isCached, cached,
			fmt.Errorf("policy bundle belongs to another tenant"))
	}
	if !bundle.Enforcing() {
		// SHADOW and CANARY are not production enforcement.
		return f.keepOnFailedReload(tenantID, isCached, cached,
			fmt.Errorf("policy bundle is %s, not ACTIVE", bundle.Activation.Status))
	}

	// Already in force under an accepted transition. Re-reading a file whose content is
	// unchanged is not a new activation, and requiring a fresh authorization for it
	// would mean a restart could not load the policy the customer already authorized.
	if current, err := f.currentTransition(ctx, tenantID); err == nil &&
		current.BundleContentHash == bundle.ContentHash {
		f.remember(tenantID, &bundle, stamp)
		return &bundle, nil
	}

	authorized, err := f.authorize(ctx, tenantID, authPath, &bundle)
	if err != nil {
		return f.keepOnFailedReload(tenantID, isCached, cached, err)
	}
	f.remember(tenantID, authorized, stamp)
	return authorized, nil
}

// authorize verifies the customer's activation and commits the transition.
//
// Nothing here changes what is enforced. The caller switches only on a nil error, so a
// failure to record the transition leaves the previous policy in force rather than
// enforcing a change nobody can attribute. The evidence and the transition are one
// commit: the old code appended evidence after switching and discarded the error, which
// allowed a state where a new policy was enforcing and no activation record existed.
func (f *FileBundles) authorize(ctx context.Context, tenantID, authPath string,
	bundle *policy.Bundle) (*policy.Bundle, error) {

	if f.Activations == nil {
		return nil, fmt.Errorf("no activation store is configured, so no policy change "+
			"can be attributed to a customer; %s stays out of force", bundle.BundleID)
	}

	rawAuth, err := os.ReadFile(authPath)
	if err != nil {
		return nil, fmt.Errorf("bundle %s carries no activation authorization: %w",
			bundle.BundleID, err)
	}
	var authorization policy.Authorization
	if err := json.Unmarshal(rawAuth, &authorization); err != nil {
		return nil, fmt.Errorf("the activation authorization is unreadable: %w", err)
	}
	if err := authorization.Validate(); err != nil {
		return nil, err
	}
	if authorization.TenantID != tenantID {
		return nil, fmt.Errorf("the activation authorization belongs to another tenant")
	}
	if err := authorization.Authorizes(bundle); err != nil {
		return nil, err
	}

	key, err := f.Activations.Key(ctx, tenantID, authorization.Signature.KeyID)
	if err != nil {
		return nil, err
	}
	at := f.now()
	if err := key.Usable(at); err != nil {
		return nil, err
	}
	if err := authorization.Verify(key.PublicKey); err != nil {
		return nil, err
	}

	prior, err := f.Activations.Current(ctx, tenantID)
	if err != nil && !errors.Is(err, policy.ErrNoTransition) {
		return nil, fmt.Errorf("the current policy transition could not be read: %w", err)
	}
	if authorization.Action == policy.ActionRollback && prior != nil &&
		authorization.PriorBundleID != prior.BundleID {
		// A rollback names both sides. One that names a predecessor other than what is
		// actually in force is describing a transition that is not the one about to
		// happen, and an audit trail built from it would be fiction.
		return nil, fmt.Errorf("the rollback names %s as the bundle in force and %s is",
			authorization.PriorBundleID, prior.BundleID)
	}

	event := f.activationEvent(authorization, bundle, prior, at)
	if _, err := f.Activations.Accept(ctx, authorization, bundle, prior, event, at); err != nil {
		if errors.Is(err, policy.ErrReplayed) {
			// The nonce is recorded. Either another process accepted this very
			// authorization — in which case the bundle it names is in force and this
			// is not a replay at all — or it is a genuinely old authorization being
			// presented again. The accepted record decides which.
			if current, currentErr := f.Activations.Current(ctx, tenantID); currentErr == nil &&
				current.BundleContentHash == bundle.ContentHash {
				return bundle, nil
			}
			return nil, fmt.Errorf("this activation authorization (nonce %s) has already "+
				"been accepted for a different bundle; a captured authorization cannot "+
				"be presented twice", authorization.Nonce)
		}
		return nil, fmt.Errorf("the policy transition could not be recorded, so it was "+
			"not applied: %w", err)
	}
	return bundle, nil
}

// activationEvent is the append-only record of the change.
//
// Activated or rolled back is what the customer's authorization says, not what this
// process happens to have witnessed. It used to be inferred from a process-local set of
// bundles this gateway had seen, which made a restart record an activation the customer
// never performed and let two replicas disagree about one tenant's history.
func (f *FileBundles) activationEvent(a policy.Authorization, b *policy.Bundle,
	prior *policy.Transition, at time.Time) evidence.Event {

	name := evidence.PolicyBundleActivated
	if a.Action == policy.ActionRollback {
		name = evidence.PolicyBundleRolledBack
	}

	payload := map[string]any{
		"bundle_id":     b.BundleID,
		"policy":        b.Policy,
		"version":       b.Version,
		"content_hash":  b.ContentHash,
		"signed_by":     b.SignedBy,
		"action":        string(a.Action),
		"actor":         a.Actor,
		"reason":        a.Reason,
		"key_id":        a.Signature.KeyID,
		"nonce":         a.Nonce,
		"authorized_at": a.AuthorizedAt.UTC().Format(time.RFC3339),
		"rules":         len(b.Rules),
	}
	if prior != nil {
		payload["previous_bundle_id"] = prior.BundleID
		payload["previous_content_hash"] = prior.BundleContentHash
	}

	return evidence.Event{
		SchemaVersion: evidence.SchemaVersion,
		EventID:       fmt.Sprintf("%s_%s", b.TenantID, a.Nonce),
		EventName:     name,
		TenantID:      b.TenantID,
		AggregateID:   b.BundleID,
		CorrelationID: b.BundleID,
		OccurredAt:    at,
		ProducedAt:    at,
		Producer:      "assurance-gateway",
		Sequence:      nextSequence(),
		Payload:       payload,
	}
}

func (f *FileBundles) currentTransition(ctx context.Context, tenantID string) (*policy.Transition, error) {
	if f.Activations == nil {
		return nil, policy.ErrNoTransition
	}
	return f.Activations.Current(ctx, tenantID)
}

func (f *FileBundles) remember(tenantID string, bundle *policy.Bundle, stamp string) {
	f.mu.Lock()
	f.cached[tenantID] = bundle
	f.stamps[tenantID] = stamp
	f.mu.Unlock()
}

func (f *FileBundles) now() time.Time {
	if f.Now != nil {
		return f.Now().UTC()
	}
	return time.Now().UTC()
}

// keepOnFailedReload keeps the bundle that is already enforcing when a candidate does
// not verify.
//
// A half-applied activation is worse than a late one: an operator who replaces a file
// with something unsigned, mistyped or belonging to another tenant should find that
// production still enforces what it was enforcing, and should find out from the log
// rather than from an outage. With nothing cached there is nothing to keep, and the
// error stands.
func (f *FileBundles) keepOnFailedReload(tenantID string, isCached bool,
	cached *policy.Bundle, err error) (*policy.Bundle, error) {

	if f.Report != nil {
		f.Report(tenantID, err)
	}
	if !isCached {
		return nil, err
	}
	return cached, nil
}

// bundleStamp identifies a file version cheaply: size and modification time.
//
// Not a hash. Hashing every bundle on every submission would put a file read on the hot
// path to detect a change that happens a few times a year, and a changed file is
// verified in full before it is trusted — the stamp only decides whether to look.
// bundleStamp is the file's size and modification time, or "absent".
//
// A missing file gets a stamp of its own rather than an error, because "the
// authorization file is not there" is a state the caller has to notice changing: a
// bundle staged before its authorization must be reconsidered once the authorization
// appears.
func bundleStamp(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return "absent"
	}
	return fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
}

// StaticSymbols maps canonical instrument identity to venue symbols.
//
// Reference data belongs to the platform (spec section 13). A file rather than a
// service because there is no instrument reference service, and an adapter guessing
// a ticker from an identifier would be inventing identity.
type StaticSymbols map[string]string

func (s StaticSymbols) SymbolFor(instrumentID string) (string, bool) {
	symbol, ok := s[instrumentID]
	return symbol, ok
}

func LoadSymbols(path string) (StaticSymbols, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m StaticSymbols
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}
