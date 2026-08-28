package gateway

import (
	"context"
	"crypto/ed25519"
	"crypto/subtle"
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
	"agentic-assurance/internal/identity"
	"agentic-assurance/internal/policy"
)

// MaxEnvelopeBytes bounds a submission body. An unbounded read is a denial of
// service against the enforcement plane, and an envelope is a small document.
const MaxEnvelopeBytes = 256 << 10

// Credentials authenticates API callers.
//
// This is not a credential management system and does not pretend to be. It is the
// smallest thing that makes the submission endpoint honestly authenticated, which it
// has to be: the read endpoints take a tenant from a header and say plainly that this
// is not authentication, but INV-001 forbids an unauthenticated workload from ever
// producing an executable order. Rotation, scoping and storage belong with a secret
// manager (spec section 35).
//
// An authenticated API caller reaches A1: we know which registered caller this is,
// and nothing attests the workload it runs in. A2 requires an SVID from the transport.
type Credentials struct {
	// byToken maps a bearer token to the API identity it authenticates.
	byToken map[string]string
}

// ParseCredentials reads "identity=token,identity=token".
func ParseCredentials(raw string) (*Credentials, error) {
	c := &Credentials{byToken: map[string]string{}}
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		name, token, ok := strings.Cut(pair, "=")
		if !ok || strings.TrimSpace(name) == "" || token == "" {
			return nil, fmt.Errorf("malformed credential entry; expected identity=token")
		}
		if len(token) < 32 {
			// A short bearer token is a guessable one, and this is the only thing
			// standing between an unauthenticated caller and an executable order.
			return nil, fmt.Errorf("credential for %q is shorter than 32 characters", name)
		}
		c.byToken[token] = strings.TrimSpace(name)
	}
	if len(c.byToken) == 0 {
		return nil, errors.New("no credentials configured")
	}
	return c, nil
}

// Identify returns the API identity a bearer token authenticates.
//
// The comparison is constant-time over every configured token rather than a map
// lookup, because a map lookup leaks through timing which prefix was right.
func (c *Credentials) Identify(token string) (string, bool) {
	if c == nil || token == "" {
		return "", false
	}
	var matched string
	for candidate, name := range c.byToken {
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(token)) == 1 {
			matched = name
		}
	}
	return matched, matched != ""
}

// presentedFrom extracts whatever identity the transport actually established.
//
// It never reads the envelope. The envelope's claimed attestation level is checked
// against this, and a claim that exceeds the evidence is refused (INV-001).
func presentedFrom(r *http.Request, creds *Credentials) identity.Presented {
	var p identity.Presented

	if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
		p.SVID = r.TLS.PeerCertificates[0]
		if len(r.TLS.PeerCertificates) > 1 {
			p.Intermediates = r.TLS.PeerCertificates[1:]
		}
		return p
	}

	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return p
	}
	if name, authenticated := creds.Identify(strings.TrimSpace(token)); authenticated {
		p.APIIdentity = name
	}
	return p
}

// SubmitHandler is POST /v1/intents.
func SubmitHandler(p *Pipeline, creds *Credentials) http.HandlerFunc {
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

// FileBundles serves signed policy bundles from a directory, one JSON file per tenant.
//
// There is no policy bundle store: Phase 4 built the lifecycle and nothing persists
// it. This is enough to run the enforcement plane and honest about what it is.
//
// It verifies the signature rather than trusting the file, and it will not activate a
// bundle itself. A gateway that signed or activated its own policy would be deciding
// what constrains it, which is the shape INV-009 exists to forbid.
type FileBundles struct {
	Dir       string
	PublicKey ed25519.PublicKey

	mu     sync.RWMutex
	cached map[string]*policy.Bundle
}

func NewFileBundles(dir, publicKeyHex string) (*FileBundles, error) {
	key, err := hex.DecodeString(strings.TrimSpace(publicKeyHex))
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("POLICY_PUBLIC_KEY must be a hex-encoded ed25519 public key")
	}
	return &FileBundles{Dir: dir, PublicKey: key, cached: map[string]*policy.Bundle{}}, nil
}

func (f *FileBundles) Active(_ context.Context, tenantID string) (*policy.Bundle, error) {
	f.mu.RLock()
	cached, ok := f.cached[tenantID]
	f.mu.RUnlock()
	if ok {
		return cached, nil
	}

	// The tenant id is a path element here, so it is checked rather than trusted.
	if tenantID == "" || strings.ContainsAny(tenantID, `/\.`) {
		return nil, fmt.Errorf("invalid tenant id")
	}

	raw, err := os.ReadFile(filepath.Join(f.Dir, tenantID+".json"))
	if err != nil {
		return nil, fmt.Errorf("no policy bundle for this tenant: %w", err)
	}

	var bundle policy.Bundle
	if err := json.Unmarshal(raw, &bundle); err != nil {
		return nil, fmt.Errorf("policy bundle is unreadable: %w", err)
	}
	if err := bundle.VerifySignature(f.PublicKey); err != nil {
		return nil, fmt.Errorf("policy bundle signature is invalid: %w", err)
	}
	if bundle.TenantID != tenantID {
		return nil, fmt.Errorf("policy bundle belongs to another tenant")
	}
	if !bundle.Enforcing() {
		// SHADOW and CANARY are not production enforcement, and a gateway that
		// promoted one would be activating its own policy.
		return nil, fmt.Errorf("policy bundle is %s, not ACTIVE", bundle.Activation.Status)
	}

	f.mu.Lock()
	f.cached[tenantID] = &bundle
	f.mu.Unlock()
	return &bundle, nil
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
