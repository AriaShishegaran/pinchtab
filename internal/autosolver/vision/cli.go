package vision

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// cliModel solves grid challenges by shelling out to an installed, already-
// authenticated agent CLI (default: `claude`). This is the no-API-key path: a
// user on a subscription needs no key — the CLI carries their auth.
//
// Invocation shape (claude): `claude -p --output-format json --allowedTools Read
// --add-dir <tmp> [--model M]` with the prompt on stdin. The prompt tells the
// agent to Read the screenshot file and reply with strict JSON. Scoping is
// intentionally minimal — `--allowedTools Read` permits ONLY the Read tool (no
// Bash/Edit/etc.) and `--add-dir <tmp>` limits filesystem access to the
// throwaway temp dir — so no blanket permission bypass
// (`--dangerously-skip-permissions`) is used. We also deliberately do NOT use
// `--bare` (it disables OAuth/keychain subscription auth).
type cliModel struct {
	binary string
	model  string
}

func newCLIModel(binary, model string) (*cliModel, error) {
	bin := strings.TrimSpace(binary)
	if bin == "" {
		bin = "claude"
	}
	if _, err := exec.LookPath(bin); err != nil {
		return nil, fmt.Errorf("vision: cli agent %q not found on PATH: %w", bin, err)
	}
	return &cliModel{binary: bin, model: strings.TrimSpace(model)}, nil
}

func (m *cliModel) Name() string {
	if m.model != "" {
		return "cli:" + m.binary + ":" + m.model
	}
	return "cli:" + m.binary
}

func (m *cliModel) SolveGrid(ctx context.Context, ch GridChallenge) (GridSolution, error) {
	if len(ch.Image) == 0 {
		return GridSolution{}, fmt.Errorf("vision cli: empty image")
	}

	tmpDir, err := os.MkdirTemp("", "pinchtab-vision-*")
	if err != nil {
		return GridSolution{}, fmt.Errorf("vision cli: temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	imgPath := filepath.Join(tmpDir, "challenge.png")
	if err := os.WriteFile(imgPath, ch.Image, 0o600); err != nil {
		return GridSolution{}, fmt.Errorf("vision cli: write image: %w", err)
	}

	prompt := "Read the image file at " + imgPath + "\n\n" + gridInstruction(ch)

	args := []string{"-p", "--output-format", "json",
		"--allowedTools", "Read",
		"--add-dir", tmpDir,
	}
	if m.model != "" {
		args = append(args, "--model", m.model)
	}

	cmd := exec.CommandContext(ctx, m.binary, args...)
	cmd.Stdin = strings.NewReader(prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		return GridSolution{}, fmt.Errorf("vision cli %q: %w (%s)", m.binary, err, truncate(detail, 240))
	}

	sol, err := parseGridSolution(stdout.String())
	if err != nil {
		return GridSolution{}, fmt.Errorf("vision cli %q: %w", m.binary, err)
	}
	return sol, nil
}
