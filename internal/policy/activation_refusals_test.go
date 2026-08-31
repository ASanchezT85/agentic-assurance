package policy

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"testing"
	"time"
)

// Every refusal the signed activation documents can return, exercised at least once.
//
// The ninth audit measured which of the platform's 106 refusal codes any test actually
// executes, using the coverage profile rather than a grep for the string — a test that
// asserts on a sentinel never mentions the code. Forty-two were never executed, and
// twenty-eight of those were here: the whole validation surface of the two documents that
// decide which policy is in force and who may put it there.
//
// They were not untested by oversight. The integration tests drive the happy path and two
// tamper cases through a real database, so `Validate` runs — its refusal branches do not.
// A branch that has never executed is a branch nobody has seen work: it may name the wrong
// field, carry the wrong code, or be unreachable.

func signedAuthorization(t *testing.T, mutate func(*Authorization)) (Authorization, ed25519.PublicKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	a := Authorization{
		SchemaVersion:     AuthorizationSchemaVersion,
		TenantID:          "tenant_x",
		BundleID:          "bundle_x",
		BundleContentHash: "hash_x",
		Action:            ActionActivate,
		Actor:             "ops@example.test",
		AuthorizedAt:      time.Now().UTC(),
		Nonce:             "nonce_x",
	}
	if err := a.Sign(private, "key_x"); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if mutate != nil {
		mutate(&a)
	}
	return a, public
}

func refusalCode(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("expected a refusal and got none")
	}
	var refusal *ActivationError
	if !errors.As(err, &refusal) {
		t.Fatalf("refusal is not an ActivationError: %T %v", err, err)
	}
	return refusal.Code
}

// The activation document: every shape it can be wrong in.
func TestEveryActivationValidationRefusal(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Authorization)
		want   string
	}{
		{"schema", func(a *Authorization) { a.SchemaVersion = "v9" }, "ACTIVATION_SCHEMA_UNSUPPORTED"},
		{"tenant", func(a *Authorization) { a.TenantID = " " }, "ACTIVATION_TENANT_MISSING"},
		{"bundle", func(a *Authorization) { a.BundleID = "" }, "ACTIVATION_BUNDLE_MISSING"},
		{"hash", func(a *Authorization) { a.BundleContentHash = "" }, "ACTIVATION_HASH_MISSING"},
		{"action", func(a *Authorization) { a.Action = "PROMOTE" }, "ACTIVATION_ACTION_INVALID"},
		{"actor", func(a *Authorization) { a.Actor = "" }, "ACTIVATION_ACTOR_MISSING"},
		{"time", func(a *Authorization) { a.AuthorizedAt = time.Time{} }, "ACTIVATION_TIME_MISSING"},
		{"nonce", func(a *Authorization) { a.Nonce = "" }, "ACTIVATION_NONCE_MISSING"},
		{"rollback without a predecessor", func(a *Authorization) {
			a.Action = ActionRollback
			a.PriorBundleID = ""
		}, "ACTIVATION_PRIOR_MISSING"},
		{"algorithm", func(a *Authorization) { a.Signature.Algorithm = "RSA" },
			"ACTIVATION_ALGORITHM_UNSUPPORTED"},
		{"signature key id", func(a *Authorization) { a.Signature.KeyID = "" },
			"ACTIVATION_KEY_ID_MISSING"},
		{"unsigned", func(a *Authorization) { a.Signature.Value = "" },
			"ACTIVATION_SIGNATURE_MISSING"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, _ := signedAuthorization(t, c.mutate)
			if got := refusalCode(t, a.Validate()); got != c.want {
				t.Errorf("refused with %s, expected %s", got, c.want)
			}
		})
	}
}

func TestAnActivationSignatureThatIsNotHexIsRefused(t *testing.T) {
	a, public := signedAuthorization(t, func(a *Authorization) { a.Signature.Value = "zzzz" })
	if got := refusalCode(t, a.Verify(public)); got != "ACTIVATION_SIGNATURE_MALFORMED" {
		t.Errorf("refused with %s", got)
	}
}

func TestAnActivationSignedByAnotherKeyIsRefused(t *testing.T) {
	a, _ := signedAuthorization(t, nil)
	other, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	if got := refusalCode(t, a.Verify(other)); got != "ACTIVATION_SIGNATURE_INVALID" {
		t.Errorf("refused with %s", got)
	}
}

// Authorizes: the document and the bundle have to be the same thing.
func TestAnAuthorizationMustMatchItsBundle(t *testing.T) {
	a, _ := signedAuthorization(t, nil)
	bundle := &Bundle{TenantID: "tenant_x", BundleID: "bundle_x", ContentHash: "hash_x"}

	if err := a.Authorizes(bundle); err != nil {
		t.Fatalf("a matching bundle was refused: %v", err)
	}
	if got := refusalCode(t, a.Authorizes(nil)); got != "ACTIVATION_NO_BUNDLE" {
		t.Errorf("no bundle refused with %s", got)
	}

	other := *bundle
	other.TenantID = "tenant_y"
	if got := refusalCode(t, a.Authorizes(&other)); got != "ACTIVATION_TENANT_MISMATCH" {
		t.Errorf("another tenant's bundle refused with %s", got)
	}

	renamed := *bundle
	renamed.BundleID = "bundle_z"
	if got := refusalCode(t, a.Authorizes(&renamed)); got != "ACTIVATION_BUNDLE_MISMATCH" {
		t.Errorf("a different bundle id refused with %s", got)
	}

	// The one that matters: the name is right and the rules have changed underneath it.
	edited := *bundle
	edited.ContentHash = "hash_y"
	if got := refusalCode(t, a.Authorizes(&edited)); got != "ACTIVATION_CONTENT_MISMATCH" {
		t.Errorf("edited content refused with %s", got)
	}
}

// The key authorization: the document that grants another key the power to activate.
func TestEveryKeyAuthorizationRefusal(t *testing.T) {
	subject, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	base := func() KeyAuthorization {
		return KeyAuthorization{
			SchemaVersion:    KeyAuthorizationSchemaVersion,
			TenantID:         "tenant_x",
			Action:           ActionRegisterKey,
			SubjectKeyID:     "act_2",
			SubjectPublicKey: hex.EncodeToString(subject),
			SubjectHolder:    "risk@example.test",
			Actor:            "ops@example.test",
			AuthorizedAt:     time.Now().UTC(),
			Nonce:            "nonce_k",
			Signature:        Signature{Algorithm: AlgorithmEd25519, KeyID: "act_1", Value: "ab"},
		}
	}

	cases := []struct {
		name   string
		mutate func(*KeyAuthorization)
		want   string
	}{
		{"schema", func(a *KeyAuthorization) { a.SchemaVersion = "v9" }, "ACTIVATION_SCHEMA_UNSUPPORTED"},
		{"tenant", func(a *KeyAuthorization) { a.TenantID = "" }, "ACTIVATION_TENANT_MISSING"},
		{"action", func(a *KeyAuthorization) { a.Action = "GRANT" }, "ACTIVATION_ACTION_INVALID"},
		{"subject key id", func(a *KeyAuthorization) { a.SubjectKeyID = "" }, "ACTIVATION_KEY_ID_MISSING"},
		{"holder", func(a *KeyAuthorization) { a.SubjectHolder = "" }, "ACTIVATION_HOLDER_MISSING"},
		{"actor", func(a *KeyAuthorization) { a.Actor = "" }, "ACTIVATION_ACTOR_MISSING"},
		{"time", func(a *KeyAuthorization) { a.AuthorizedAt = time.Time{} }, "ACTIVATION_TIME_MISSING"},
		{"nonce", func(a *KeyAuthorization) { a.Nonce = "" }, "ACTIVATION_NONCE_MISSING"},
		{"algorithm", func(a *KeyAuthorization) { a.Signature.Algorithm = "RSA" },
			"ACTIVATION_ALGORITHM_UNSUPPORTED"},
		{"unsigned", func(a *KeyAuthorization) { a.Signature.Value = "" },
			"ACTIVATION_SIGNATURE_MISSING"},
		{"self-signed", func(a *KeyAuthorization) { a.Signature.KeyID = a.SubjectKeyID },
			"ACTIVATION_KEY_SELF_SIGNED"},
		{"an empty window", func(a *KeyAuthorization) {
			from := time.Now().UTC()
			until := from.Add(-time.Hour)
			a.SubjectValidFrom, a.SubjectValidUntil = &from, &until
		}, "ACTIVATION_WINDOW_EMPTY"},
		{"a public key that is not hex", func(a *KeyAuthorization) { a.SubjectPublicKey = "zz" },
			"ACTIVATION_KEY_MALFORMED"},
		{"a private key", func(a *KeyAuthorization) {
			_, private, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatalf("keygen: %v", err)
			}
			a.SubjectPublicKey = hex.EncodeToString(private)
		}, "ACTIVATION_KEY_IS_PRIVATE"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := base()
			c.mutate(&a)
			if got := refusalCode(t, a.Validate()); got != c.want {
				t.Errorf("refused with %s, expected %s", got, c.want)
			}
		})
	}
}

// A key's own window, which decides whether it may authorize anything at this instant.
func TestAnActivationKeyIsUsableOnlyInsideItsWindow(t *testing.T) {
	now := time.Now().UTC()
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)

	revoked := ActivationKey{KeyID: "k", Status: "REVOKED", ValidFrom: past}
	if got := refusalCode(t, revoked.Usable(now)); got != "ACTIVATION_KEY_REVOKED" {
		t.Errorf("a revoked key refused with %s", got)
	}

	early := ActivationKey{KeyID: "k", Status: "ACTIVE", ValidFrom: future}
	if got := refusalCode(t, early.Usable(now)); got != "ACTIVATION_KEY_NOT_YET_VALID" {
		t.Errorf("a key that is not yet valid refused with %s", got)
	}

	expired := ActivationKey{KeyID: "k", Status: "ACTIVE", ValidFrom: past, ValidUntil: &past}
	if got := refusalCode(t, expired.Usable(now)); got != "ACTIVATION_KEY_EXPIRED" {
		t.Errorf("an expired key refused with %s", got)
	}

	live := ActivationKey{KeyID: "k", Status: "ACTIVE", ValidFrom: past, ValidUntil: &future}
	if err := live.Usable(now); err != nil {
		t.Errorf("a usable key was refused: %v", err)
	}
}
