// Package canonicaljson turns JSON into one deterministic byte sequence.
//
// One document, one representation: keys sorted, no whitespace, number literals
// verbatim. Two things depend on it and they are not the same thing, which is why it
// lives here rather than inside either of them.
//
// Signing an envelope needs it because a signature is over bytes and a client's JSON
// encoder is not the platform's — key order and spacing must not decide whether a
// signature verifies.
//
// Archiving evidence needs it because an archived event's hash must change when the
// event changes and not when it is re-serialised.
//
// They differ in one respect, and that difference was a defect. Envelope signing removes
// the top-level `signature` field, because a signature cannot cover itself. Retention
// reused that function to canonicalize event payloads, so a payload containing a field
// called `signature` had it deleted before hashing: two archived events differing only in
// that field produced identical bytes, and an archive claiming to cover the full
// immutable event did not.
//
// So the generic form removes nothing. Domain-specific removal belongs to the domain that
// needs it, one layer up.
package canonicaljson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"unicode/utf8"
)

// Version is part of the contract. A change to the algorithm is a new version rather
// than a patch, because every signature and every archive hash already produced assumed
// the old one.
const Version = "v0.1"

// Canonical returns the deterministic form of a JSON document.
//
// Every key is preserved. Numbers keep their literal text: parsing one into a float and
// printing it back is how a signature that was correct stops verifying, and how an
// amount that was exact stops being.
func Canonical(raw []byte) ([]byte, error) {
	document, err := decode(raw)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := write(&out, document); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// CanonicalObject is Canonical for a document that must be a JSON object, returning the
// decoded object alongside so a caller can inspect it without decoding twice.
func CanonicalObject(raw []byte) (map[string]any, error) {
	document, err := decode(raw)
	if err != nil {
		return nil, err
	}
	object, ok := document.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("not a JSON object")
	}
	return object, nil
}

// CanonicalizeObject writes the canonical form of an already-decoded object.
func CanonicalizeObject(object map[string]any) ([]byte, error) {
	var out bytes.Buffer
	if err := write(&out, object); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func decode(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	// Numbers stay as their literal text.
	decoder.UseNumber()

	var document any
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("not JSON: %w", err)
	}
	return document, nil
}

func write(out *bytes.Buffer, value any) error {
	switch v := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)

		out.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := writeString(out, key); err != nil {
				return err
			}
			out.WriteByte(':')
			if err := write(out, v[key]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
		return nil

	case []any:
		out.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := write(out, item); err != nil {
				return err
			}
		}
		out.WriteByte(']')
		return nil

	case json.Number:
		out.WriteString(v.String())
		return nil

	case string:
		return writeString(out, v)

	case bool:
		if v {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
		return nil

	case nil:
		out.WriteString("null")
		return nil

	default:
		return fmt.Errorf("value of type %T cannot appear in canonical JSON", value)
	}
}

// writeString escapes exactly what JSON requires and nothing else, so two encoders that
// disagree about optional escaping cannot disagree about the canonical form.
func writeString(out *bytes.Buffer, s string) error {
	if !utf8.ValidString(s) {
		return fmt.Errorf("string is not valid UTF-8")
	}
	encoded, err := json.Marshal(s)
	if err != nil {
		return err
	}
	out.Write(encoded)
	return nil
}
