package trace

import "encoding/json"

// DecryptBody walks a JSON body and decrypts every EncryptField marker it
// finds under dataKey, returning the (possibly) decrypted body and whether
// EVERY marker found was successfully opened. ok is true when the body
// carries no marker at all, so a caller that only wants "safe to treat as
// final" can check ok alone.
//
// A marker DecryptField cannot open — dataKey is nil (no team key
// resolved), or it is the wrong key — is left exactly as it was in the
// returned body; ok is false in that case. Callers differ on what an
// unresolved marker means: retrace/diff compares it literally, same as
// today's destroy-mode marker, while retrace/replay must refuse to serve
// it as though it were the real value (see that package's fail-closed
// doc). Both read ok to make that call, rather than each re-deriving
// "was there a marker left in this body" from the string.
//
// A body that is not JSON passes through unchanged with ok=true: there is
// nothing here for a marker to hide in.
func DecryptBody(body string, dataKey []byte) (out string, ok bool) {
	if body == "" {
		return body, true
	}
	var v any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		return body, true
	}
	v, changed, ok := decryptTreeValue(v, dataKey)
	if !changed {
		return body, ok
	}
	b, err := json.Marshal(v)
	if err != nil {
		return body, ok
	}
	return string(b), ok
}

// decryptTreeValue mirrors Redactor.redactValue's tree walk in reverse: an
// encrypted string leaf decodes back to its original JSON type when its
// plaintext is itself valid JSON, matching applyValue's canonical-encoding
// seal on the write side.
func decryptTreeValue(v any, dataKey []byte) (result any, changed bool, ok bool) {
	switch t := v.(type) {
	case string:
		if !IsEncrypted(t) {
			return v, false, true
		}
		if len(dataKey) == 0 {
			return v, false, false
		}
		plain, err := DecryptField(dataKey, t)
		if err != nil {
			return v, false, false
		}
		var decoded any
		if err := json.Unmarshal([]byte(plain), &decoded); err == nil {
			return decoded, true, true
		}
		return plain, true, true
	case map[string]any:
		changed, ok := false, true
		for k, val := range t {
			nv, c, o := decryptTreeValue(val, dataKey)
			if c {
				t[k] = nv
				changed = true
			}
			if !o {
				ok = false
			}
		}
		return t, changed, ok
	case []any:
		changed, ok := false, true
		for i, val := range t {
			nv, c, o := decryptTreeValue(val, dataKey)
			if c {
				t[i] = nv
				changed = true
			}
			if !o {
				ok = false
			}
		}
		return t, changed, ok
	default:
		return v, false, true
	}
}
