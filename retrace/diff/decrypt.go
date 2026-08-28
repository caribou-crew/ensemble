package diff

import (
	"encoding/json"

	"github.com/caribou-crew/ensemble/core/trace"
)

// decryptHops returns hops with every encrypt-mode marker in a Req/Resp
// body decrypted under dataKey, so a real value change behind two
// encrypted markers is a real wire diff again — not two identical markers
// comparing equal. A nil dataKey (no team key resolved, or nothing on this
// side was ever encrypted) is a no-op: hops pass through unchanged.
func decryptHops(hops []trace.Hop, dataKey []byte) []trace.Hop {
	if len(dataKey) == 0 || len(hops) == 0 {
		return hops
	}
	out := make([]trace.Hop, len(hops))
	for i, h := range hops {
		h.Req.Body = decryptBody(h.Req.Body, dataKey)
		h.Resp.Body = decryptBody(h.Resp.Body, dataKey)
		out[i] = h
	}
	return out
}

// decryptBody walks a JSON body and decrypts any $enc:v1: marker it finds.
// A marker that fails to decrypt (this dataKey does not open it) is left
// exactly as it was — the comparison downstream then sees the marker on
// both sides, same as today's destroy-mode masking. Non-JSON bodies pass
// through untouched, matching trace.Redactor's own "not JSON, not touched"
// rule.
func decryptBody(body string, dataKey []byte) string {
	if body == "" {
		return body
	}
	var v any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		return body
	}
	v, changed := decryptValue(v, dataKey)
	if !changed {
		return body
	}
	out, err := json.Marshal(v)
	if err != nil {
		return body
	}
	return string(out)
}

// decryptValue mirrors trace.Redactor.redactValue's tree walk in reverse:
// any encrypted string leaf is replaced by its decrypted value, decoded
// back to its original JSON type when the plaintext is itself valid JSON
// (trace.EncryptField seals the field's canonical JSON encoding, per
// core/trace's applyValue).
func decryptValue(v any, dataKey []byte) (any, bool) {
	switch t := v.(type) {
	case string:
		if !trace.IsEncrypted(t) {
			return v, false
		}
		plain, err := trace.DecryptField(dataKey, t)
		if err != nil {
			return v, false
		}
		var decoded any
		if err := json.Unmarshal([]byte(plain), &decoded); err == nil {
			return decoded, true
		}
		return plain, true
	case map[string]any:
		changed := false
		for k, val := range t {
			nv, c := decryptValue(val, dataKey)
			if c {
				t[k] = nv
				changed = true
			}
		}
		return t, changed
	case []any:
		changed := false
		for i, val := range t {
			nv, c := decryptValue(val, dataKey)
			if c {
				t[i] = nv
				changed = true
			}
		}
		return t, changed
	default:
		return v, false
	}
}
