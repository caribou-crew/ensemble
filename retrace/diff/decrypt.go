package diff

import "github.com/caribou-crew/ensemble/core/trace"

// decryptHops returns hops with every encrypt-mode marker in a Req/Resp
// body decrypted under dataKey, so a real value change behind two
// encrypted markers is a real wire diff again — not two identical markers
// comparing equal. A nil dataKey (no team key resolved, or nothing on this
// side was ever encrypted) is a no-op: hops pass through unchanged. A
// marker that fails to decrypt (wrong key) is left exactly as it was —
// trace.DecryptBody's `ok` is deliberately ignored here: the comparison
// downstream then sees the marker on both sides, same as today's
// destroy-mode masking, rather than erroring the whole diff.
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

func decryptBody(body string, dataKey []byte) string {
	out, _ := trace.DecryptBody(body, dataKey)
	return out
}
