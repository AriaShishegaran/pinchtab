package solvers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"time"

	"github.com/pinchtab/pinchtab/internal/autosolver"
)

// solverDebugDir returns the directory for visual-solver debug artifacts when
// PINCHTAB_SOLVER_DEBUG_DIR is set, else "" (debug disabled). Used to dump the
// cropped challenge image + the vision solution per round so the geometry/vision
// pipeline is observable and tunable during live runs.
func solverDebugDir() string { return os.Getenv("PINCHTAB_SOLVER_DEBUG_DIR") }

// dumpSolverDebug best-effort writes a challenge image + a JSON meta sidecar.
func dumpSolverDebug(dir, tag string, img []byte, meta any) {
	if dir == "" {
		return
	}
	_ = os.MkdirAll(dir, 0o755)
	stamp := time.Now().Format("150405.000")
	if len(img) > 0 {
		_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s-%s.png", tag, stamp)), img, 0o644)
	}
	if meta != nil {
		if raw, err := json.MarshalIndent(meta, "", "  "); err == nil {
			_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s-%s.json", tag, stamp)), raw, 0o644)
		}
	}
}

// box is a CSS-pixel bounding rectangle (matches getBoundingClientRect / the
// coordinate space chromedp mouse input uses, so clicks need no DPR scaling).
type box struct{ x, y, width, height float64 }

// gridLayout describes where, as fractions of a challenge iframe's box, the
// image grid and the Verify/Next button sit. hCaptcha and reCAPTCHA share the
// same structure with slightly different proportions.
type gridLayout struct {
	marginX     float64 // left/right margin around the grid
	topFrac     float64 // grid top (below the prompt header)
	botFrac     float64 // grid bottom (above the footer)
	verifyXFrac float64 // Verify button center X
	verifyYFrac float64 // Verify button center Y
}

// tile maps a 1-based row-major tile index to its center (CSS px).
func (g gridLayout) tile(b box, rows, cols, idx int) (float64, float64, bool) {
	if idx < 1 || idx > rows*cols {
		return 0, 0, false
	}
	gridX := b.x + b.width*g.marginX
	gridY := b.y + b.height*g.topFrac
	gridW := b.width * (1 - 2*g.marginX)
	gridH := b.height * (g.botFrac - g.topFrac)
	cellW := gridW / float64(cols)
	cellH := gridH / float64(rows)
	r := (idx - 1) / cols
	c := (idx - 1) % cols
	return gridX + (float64(c)+0.5)*cellW, gridY + (float64(r)+0.5)*cellH, true
}

// verify returns the Verify/Next button center (CSS px).
func (g gridLayout) verify(b box) (float64, float64) {
	return b.x + b.width*g.verifyXFrac, b.y + b.height*g.verifyYFrac
}

// humanClick clicks at (x,y) with a small random offset + human-like pacing.
func humanClick(rng *rand.Rand, ctx context.Context, executor autosolver.ActionExecutor, x, y float64) {
	jx := x + (rng.Float64()-0.5)*6
	jy := y + (rng.Float64()-0.5)*6
	_ = executor.Click(ctx, jx, jy)
	time.Sleep(time.Duration(300+rng.Intn(400)) * time.Millisecond)
}

// devicePixelRatio reads window.devicePixelRatio (>=1).
func devicePixelRatio(ctx context.Context, executor autosolver.ActionExecutor) float64 {
	var dpr float64
	if err := executor.Evaluate(ctx, `window.devicePixelRatio || 1`, &dpr); err != nil || dpr <= 0 {
		return 1
	}
	return dpr
}

// cropPNG crops the screenshot to the challenge box (box is CSS px; the
// screenshot is device px → scale by dpr). Returns the original bytes on any
// decode/crop failure so the vision model still receives a usable image.
func cropPNG(shot []byte, b box, dpr float64) []byte {
	if dpr <= 0 {
		dpr = 1
	}
	img, err := png.Decode(bytes.NewReader(shot))
	if err != nil {
		return shot
	}
	bounds := img.Bounds()
	pad := int(8 * dpr)
	x0 := clamp(int(b.x*dpr)-pad, bounds.Min.X, bounds.Max.X)
	y0 := clamp(int(b.y*dpr)-pad, bounds.Min.Y, bounds.Max.Y)
	x1 := clamp(int((b.x+b.width)*dpr)+pad, bounds.Min.X, bounds.Max.X)
	y1 := clamp(int((b.y+b.height)*dpr)+pad, bounds.Min.Y, bounds.Max.Y)
	if x1-x0 < 20 || y1-y0 < 20 {
		return shot
	}
	type subImager interface {
		SubImage(r image.Rectangle) image.Image
	}
	si, ok := img.(subImager)
	if !ok {
		return shot
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, si.SubImage(image.Rect(x0, y0, x1, y1))); err != nil {
		return shot
	}
	return buf.Bytes()
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// evalBox runs a JS expression that returns {x,y,width,height} (or null) and
// converts it to a *box (nil when null/too small).
func evalBox(ctx context.Context, executor autosolver.ActionExecutor, expr string) (*box, error) {
	var raw map[string]float64
	if err := executor.Evaluate(ctx, expr, &raw); err != nil {
		return nil, err
	}
	if raw == nil || raw["width"] < 50 {
		return nil, nil
	}
	return &box{x: raw["x"], y: raw["y"], width: raw["width"], height: raw["height"]}, nil
}
