package executor

import (
	"encoding/json"
	"strings"
)

// parseList parses a dynamic for_each source string into instance values. A
// string that parses as a JSON array of strings is used directly; otherwise the
// string is split on newlines. In both cases entries are trimmed and empties
// dropped, so trailing newlines or blank lines do not spawn empty instances.
func parseList(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if strings.HasPrefix(s, "[") {
		var arr []string
		if err := json.Unmarshal([]byte(s), &arr); err == nil {
			return trimDropEmpty(arr)
		}
		// Not a JSON string array; fall through to newline splitting.
	}
	return trimDropEmpty(strings.Split(s, "\n"))
}

func trimDropEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
