package identity

import (
	"context"
	"errors"
	"testing"
	"time"
)

// SIGNATURE_MISSING: the fail-closed answer when there is no envelope to verify.
//
// Found by the ninth audit's coverage census. It is one line and it had never run: the
// tests that matter all pass a real envelope, so the branch that refuses the absence of
// one was reached by nothing.
func TestVerifyingNothingIsRefused(t *testing.T) {
	err := VerifyEnvelopeSignature(context.Background(), nil, nil, nil, time.Now().UTC())
	if err == nil {
		t.Fatal("an absent envelope verified successfully; a platform that cannot check a " +
			"signature must not decide the signature is fine")
	}
	var sig *SignatureError
	if !errors.As(err, &sig) {
		t.Fatalf("refusal is not a SignatureError: %T %v", err, err)
	}
	if sig.Code != "SIGNATURE_MISSING" {
		t.Errorf("refused with %s, expected SIGNATURE_MISSING", sig.Code)
	}
}
