package vision

import (
	"encoding/json"
	"fmt"
	"strings"
)

// parseGridSolution extracts a GridSolution from arbitrary model text. It is
// defensive: models wrap JSON in prose or ```json fences, and the `claude
// --output-format json` envelope nests the answer under {"result":"..."}.
func parseGridSolution(text string) (GridSolution, error) {
	clean := strings.TrimSpace(text)
	if clean == "" {
		return GridSolution{}, fmt.Errorf("vision: empty model output")
	}

	// 1) If this is a claude CLI JSON envelope, unwrap the inner result text.
	if inner, ok := unwrapClaudeEnvelope(clean); ok {
		clean = inner
	}

	// 2) Find the first balanced JSON object that has a "tiles" or "prompt" key.
	obj := firstJSONObject(clean)
	if obj == "" {
		return GridSolution{}, fmt.Errorf("vision: no JSON object in model output: %s", truncate(clean, 200))
	}

	var sol GridSolution
	if err := json.Unmarshal([]byte(obj), &sol); err != nil {
		return GridSolution{}, fmt.Errorf("vision: decode solution: %w (raw: %s)", err, truncate(obj, 200))
	}
	if strings.TrimSpace(sol.Done) == "" {
		sol.Done = "verify"
	}
	return sol, nil
}

// unwrapClaudeEnvelope pulls the assistant text out of the claude CLI
// `--output-format json` envelope: {"type":"result","subtype":"success",
// "result":"<assistant text>", ...}. Returns (inner, true) when matched.
func unwrapClaudeEnvelope(s string) (string, bool) {
	var env struct {
		Result  string `json:"result"`
		IsError bool   `json:"is_error"`
	}
	if err := json.Unmarshal([]byte(s), &env); err != nil {
		return "", false
	}
	if strings.TrimSpace(env.Result) == "" {
		return "", false
	}
	return env.Result, true
}

// firstJSONObject returns the first top-level balanced {...} run in s, ignoring
// braces inside strings. Returns "" when none is found.
func firstJSONObject(s string) string {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	depth := 0
	inStr := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
