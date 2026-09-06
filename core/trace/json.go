package trace

import (
	"encoding/json"
	"io"
	"strings"
)

// decodeJSON preserves exact numeric values while rewriting capture bodies.
// Like json.Unmarshal it requires one complete value, with no trailing data.
func decodeJSON(body string) (any, bool) {
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, false
	}
	return value, true
}
