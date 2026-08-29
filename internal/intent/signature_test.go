package intent

import "testing"

// The canonical form is a contract. These are its test vectors: an agent in another
// language must be able to reproduce them from the three rules in signature.go.
func TestCanonicalIsDeterministicAndOrderIndependent(t *testing.T) {
	a := []byte(`{"b":2,"a":1,"signature":{"value":"x"}}`)
	b := []byte(`{"a":1,"b":2}`)

	canonA, err := Canonical(a)
	if err != nil {
		t.Fatalf("canonical a: %v", err)
	}
	canonB, err := Canonical(b)
	if err != nil {
		t.Fatalf("canonical b: %v", err)
	}
	if string(canonA) != string(canonB) {
		t.Errorf("key order or the signature changed the bytes:\n a: %s\n b: %s", canonA, canonB)
	}
	if want := `{"a":1,"b":2}`; string(canonA) != want {
		t.Errorf("canonical = %s, want %s", canonA, want)
	}
}

// Numbers travel as written. Re-encoding 1200 as 1200.0, or 0.1 through a float, is
// how a signature that was correct stops verifying.
func TestCanonicalKeepsNumberLiterals(t *testing.T) {
	for _, literal := range []string{"1200", "1200.00", "0.1", "1e3", "-0"} {
		raw := []byte(`{"n":` + literal + `}`)
		canonical, err := Canonical(raw)
		if err != nil {
			t.Fatalf("%s: %v", literal, err)
		}
		if want := `{"n":` + literal + `}`; string(canonical) != want {
			t.Errorf("canonical = %s, want %s", canonical, want)
		}
	}
}

func TestCanonicalSortsNestedKeysAndKeepsArrayOrder(t *testing.T) {
	raw := []byte(`{"z":{"b":true,"a":null},"list":[3,1,2]}`)
	canonical, err := Canonical(raw)
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	want := `{"list":[3,1,2],"z":{"a":null,"b":true}}`
	if string(canonical) != want {
		t.Errorf("canonical = %s, want %s", canonical, want)
	}
}
