// Package vision provides a small, provider-agnostic vision-model client used by
// the autosolver's visual CAPTCHA solvers (hCaptcha / reCAPTCHA image grids).
//
// Two transports are supported so the solver works whether or not the operator
// has an LLM API key:
//
//   - CLI passthrough (default): shells out to an already-installed, already-
//     authenticated agent CLI (e.g. `claude`), so a user on a subscription needs
//     no API key. The image is written to a temp file and the CLI is asked to
//     read it and return strict JSON.
//   - API: a direct Anthropic/OpenAI vision call when an API key is configured.
//
// The model is asked for TILE INDICES (and the prompt text it read), never raw
// pixel coordinates — vision models are reliable at "which of these 9 tiles" but
// unreliable at precise pixel localization. The caller maps indices to click
// points using the known grid geometry.
package vision

import (
	"context"
	"fmt"
	"strings"
)

// GridChallenge is a screenshot of an image-grid CAPTCHA to classify.
type GridChallenge struct {
	// Image is the PNG screenshot of the challenge (full viewport is fine; the
	// model is told where the grid is via Rows/Cols + the instruction).
	Image []byte
	// Rows and Cols describe the expected grid (hCaptcha is typically 3x3).
	Rows int
	Cols int
	// Hint is optional extra context for the model (e.g. provider name).
	Hint string
}

// GridSolution is the model's answer for a grid challenge.
type GridSolution struct {
	// Prompt is the challenge instruction the model read from the image
	// (e.g. "Please click each image containing a motorcycle"). Diagnostic.
	Prompt string `json:"prompt"`
	// Tiles are the 1-based indices (row-major, left-to-right, top-to-bottom)
	// of the tiles that match the instruction.
	Tiles []int `json:"tiles"`
	// Done is the control the solver should press after selecting: "verify",
	// "next", or "skip". Defaults to "verify" when empty.
	Done string `json:"done"`
	// NoMatch is true when the model believes no tile matches (rare; some
	// challenges legitimately have zero matches and you just press skip/verify).
	NoMatch bool `json:"noMatch"`
	// Confidence is the model's self-reported confidence (0..1), best-effort.
	Confidence float64 `json:"confidence"`
}

// Model classifies image-grid CAPTCHA challenges.
type Model interface {
	// SolveGrid returns which tiles match the challenge instruction.
	SolveGrid(ctx context.Context, ch GridChallenge) (GridSolution, error)
	// Name identifies the transport+model for logs.
	Name() string
}

// Config selects and configures the vision transport.
type Config struct {
	// Provider: "cli" (default when no APIKey), "anthropic", or "openai".
	Provider string
	// Model name. Optional for CLI (the CLI uses its default); for API it
	// should be a vision-capable model.
	Model string
	// APIKey, when set, enables the API transport for anthropic/openai.
	APIKey string
	// Command is the CLI binary for passthrough mode (default "claude").
	Command string
	// BaseURL overrides the API endpoint (optional; for proxies/self-host).
	BaseURL string
}

// New builds a vision Model from config. Selection rules:
//   - explicit Provider "anthropic"/"openai" with an APIKey -> API transport;
//   - otherwise -> CLI passthrough (wraps an installed authenticated agent),
//     which needs no API key. Returns an error only if the chosen transport is
//     unusable (e.g. API provider selected without a key, or CLI binary absent).
func New(cfg Config) (Model, error) {
	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))

	useAPI := cfg.APIKey != "" && (provider == "anthropic" || provider == "openai")
	if useAPI {
		return newAPIModel(provider, cfg)
	}

	// CLI passthrough is the default and the no-API-key path.
	cmd := strings.TrimSpace(cfg.Command)
	if cmd == "" {
		cmd = "claude"
	}
	return newCLIModel(cmd, cfg.Model)
}

// gridInstruction is the shared task prompt sent to whichever transport. It
// pins the output to strict JSON the parser can extract.
func gridInstruction(ch GridChallenge) string {
	rows := ch.Rows
	if rows <= 0 {
		rows = 3
	}
	cols := ch.Cols
	if cols <= 0 {
		cols = 3
	}
	n := rows * cols
	var b strings.Builder
	b.WriteString("You are solving an image-selection CAPTCHA shown in the screenshot.\n")
	if strings.TrimSpace(ch.Hint) != "" {
		b.WriteString("Context: " + strings.TrimSpace(ch.Hint) + "\n")
	}
	fmt.Fprintf(&b, "The challenge shows an instruction line and a %dx%d grid of %d image tiles.\n", rows, cols, n)
	fmt.Fprintf(&b, "Number the tiles 1..%d in reading order (row by row, left to right).\n", n)
	b.WriteString("Read the instruction, then decide which tiles satisfy it.\n")
	b.WriteString("Respond with ONLY a single JSON object, no prose, no markdown fences:\n")
	b.WriteString(`{"prompt":"<the instruction text you read>","tiles":[<matching tile numbers>],"done":"verify","noMatch":false,"confidence":<0..1>}`)
	b.WriteString("\nRules: tiles is an array of integers (empty if none match). ")
	b.WriteString("Use \"done\":\"verify\" unless the instruction says to keep selecting until none remain, in which case use \"next\". ")
	b.WriteString("Set noMatch true only when no tile matches.")
	return b.String()
}
