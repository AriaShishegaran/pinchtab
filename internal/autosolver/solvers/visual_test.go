package solvers

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/pinchtab/pinchtab/internal/autosolver"
	"github.com/pinchtab/pinchtab/internal/autosolver/vision"
)

func TestGridLayoutTile(t *testing.T) {
	b := box{x: 100, y: 100, width: 300, height: 450}
	// Center tile (5) of a 3x3 grid should sit at the middle of the grid region.
	cx, cy, ok := hcLayout.tile(b, 3, 3, 5)
	if !ok {
		t.Fatal("tile 5 should be valid")
	}
	gridMidX := b.x + b.width*0.5
	if math.Abs(cx-gridMidX) > 1 {
		t.Fatalf("center tile X = %.1f, want ~%.1f", cx, gridMidX)
	}
	wantMidY := b.y + b.height*(hcGridMid())
	if math.Abs(cy-wantMidY) > 1 {
		t.Fatalf("center tile Y = %.1f, want ~%.1f", cy, wantMidY)
	}
	// Out-of-range indices are rejected.
	if _, _, ok := hcLayout.tile(b, 3, 3, 0); ok {
		t.Fatal("index 0 should be invalid")
	}
	if _, _, ok := hcLayout.tile(b, 3, 3, 10); ok {
		t.Fatal("index 10 should be invalid")
	}
}

func hcGridMid() float64 { return (hcLayout.topFrac + hcLayout.botFrac) / 2 }

// --- fakes ---

type fakeVision struct{ tiles []int }

func (v *fakeVision) Name() string { return "fake" }
func (v *fakeVision) SolveGrid(_ context.Context, _ vision.GridChallenge) (vision.GridSolution, error) {
	return vision.GridSolution{Tiles: v.tiles, Done: "verify"}, nil
}

var (
	_ autosolver.Page           = (*fakePage)(nil)
	_ autosolver.ActionExecutor = (*fakeExec)(nil)
)

type fakePage struct{ shot []byte }

func (p *fakePage) URL() string                  { return "https://example.test/apply" }
func (p *fakePage) Title() string                { return "Apply" }
func (p *fakePage) HTML() (string, error)        { return `<div class="h-captcha"></div>`, nil }
func (p *fakePage) Screenshot() ([]byte, error)  { return p.shot, nil }

type fakeExec struct {
	clicks      [][2]float64
	responseSeq []float64 // values returned for successive hasResponse evals
	responseIdx int
}

func (e *fakeExec) Click(_ context.Context, x, y float64) error {
	e.clicks = append(e.clicks, [2]float64{x, y})
	return nil
}
func (e *fakeExec) Type(context.Context, string) error                       { return nil }
func (e *fakeExec) WaitFor(context.Context, string, time.Duration) error     { return nil }
func (e *fakeExec) Navigate(context.Context, string) error                   { return nil }

func (e *fakeExec) Evaluate(_ context.Context, expr string, result interface{}) error {
	var canned any
	switch {
	case strings.Contains(expr, "devicePixelRatio"):
		canned = 1.0
	case strings.Contains(expr, "getResponse") || strings.Contains(expr, "h-captcha-response"):
		v := 0.0
		if e.responseIdx < len(e.responseSeq) {
			v = e.responseSeq[e.responseIdx]
		}
		e.responseIdx++
		canned = v
	case strings.Contains(expr, "checkbox"):
		canned = nil // no separate checkbox; challenge already open
	case strings.Contains(expr, "iframe"):
		// challengeBox: the open challenge is always present in this fixture;
		// the loop exits via hasResponse returning a token after the clicks.
		canned = map[string]float64{"x": 100, "y": 100, "width": 300, "height": 450}
	default:
		canned = nil
	}
	raw, _ := json.Marshal(canned)
	return json.Unmarshal(raw, result)
}

func TestHCaptchaSolveClicksTilesAndExits(t *testing.T) {
	page := &fakePage{shot: []byte("not-a-real-png")} // cropPNG falls back to raw bytes
	exec := &fakeExec{
		// First hasResponse (after round-1 clicks) returns a token length > 0.
		responseSeq: []float64{12},
	}
	s := &HCaptcha{Vision: &fakeVision{tiles: []int{1, 5, 9}}, MaxRounds: 3}

	res, err := s.Solve(context.Background(), page, exec)
	if err != nil {
		t.Fatalf("solve: %v", err)
	}
	if !res.Solved {
		t.Fatalf("expected solved, got %+v", res)
	}
	// 3 tile clicks + 1 verify click.
	if len(exec.clicks) != 4 {
		t.Fatalf("expected 4 clicks (3 tiles + verify), got %d: %v", len(exec.clicks), exec.clicks)
	}
	// Verify click should be bottom-right of the box (~358, ~523), allowing jitter.
	verify := exec.clicks[3]
	wantVX := 100 + 300*hcLayout.verifyXFrac
	wantVY := 100 + 450*hcLayout.verifyYFrac
	if math.Abs(verify[0]-wantVX) > 5 || math.Abs(verify[1]-wantVY) > 5 {
		t.Fatalf("verify click = %v, want ~(%.0f,%.0f)", verify, wantVX, wantVY)
	}
	// First tile (index 1) center is top-left cell.
	tile1 := exec.clicks[0]
	wantX, wantY, _ := hcLayout.tile(box{100, 100, 300, 450}, 3, 3, 1)
	if math.Abs(tile1[0]-wantX) > 5 || math.Abs(tile1[1]-wantY) > 5 {
		t.Fatalf("tile1 click = %v, want ~(%.0f,%.0f)", tile1, wantX, wantY)
	}
}

func TestHCaptchaNoVisionFailsClosed(t *testing.T) {
	s := &HCaptcha{}
	res, err := s.Solve(context.Background(), &fakePage{}, &fakeExec{})
	if err == nil || res.Solved {
		t.Fatalf("expected failure without vision model, got %+v err=%v", res, err)
	}
}
