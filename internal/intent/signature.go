package intent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// Canonical bytes for envelope signing.
//
// An envelope carries a signature field and nothing verified it. The tenant came from
// the transport credential, but `agent_id` was a claim in the body: the platform knew
// which customer was calling and took the agent's word for which agent it was. A
// signature is where that claim stops being caller-supplied data.
//
// # The canonical form, v0.1
//
// Signing pretty-printed JSON signs a formatter. This defines one deterministic
// representation, small enough that an agent in any language can reproduce it:
//
//  1. parse the envelope as JSON, preserving number literals exactly as written;
//  2. remove the top-level "signature" member;
//  3. serialize with object keys sorted by Unicode code point, no insignificant
//     whitespace, and every number emitted verbatim as it appeared in the input.
//
// Numbers are the trap this avoids. Re-encoding 1200 as 1200.0, or 0.1 through a
// float, changes the bytes and breaks a signature that was correct — so the literal
// travels unchanged rather than through a float64.
//
// The algorithm is part of the contract. A change to it is a new version, not a patch,
// and CanonicalVersion is what a producer states it used.
const CanonicalVersion = "v0.1"

// Canonical returns the bytes a signature covers.
func Canonical(raw []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()

	var document any
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("envelope is not JSON: %w", err)
	}

	object, ok := document.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("envelope is not a JSON object")
	}
	// The signature cannot cover itself.
	delete(object, "signature")

	var out bytes.Buffer
	if err := writeCanonical(&out, object); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func writeCanonical(out *bytes.Buffer, value any) error {
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
			if err := writeCanonicalString(out, key); err != nil {
				return err
			}
			out.WriteByte(':')
			if err := writeCanonical(out, v[key]); err != nil {
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
			if err := writeCanonical(out, item); err != nil {
				return err
			}
		}
		out.WriteByte(']')
		return nil

	case json.Number:
		// Verbatim. Parsing this into a float and printing it back is how a
		// signature that was correct stops verifying.
		out.WriteString(v.String())
		return nil

	case string:
		return writeCanonicalString(out, v)

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
		return fmt.Errorf("value of type %T cannot appear in a canonical envelope", value)
	}
}

func writeCanonicalString(out *bytes.Buffer, s string) error {
	// encoding/json's own escaping, with HTML escaping off so the bytes depend on the
	// string rather than on where it might be displayed.
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(s); err != nil {
		return err
	}
	out.Write(bytes.TrimRight(buf.Bytes(), "\n"))
	return nil
}
