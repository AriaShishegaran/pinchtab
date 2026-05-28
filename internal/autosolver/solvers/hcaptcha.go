package solvers

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/pinchtab/pinchtab/internal/autosolver"
	"github.com/pinchtab/pinchtab/internal/autosolver/vision"
)

// HCaptcha solves hCaptcha image-grid challenges with a vision model. It detects
// the open challenge iframe, screenshots + crops it, asks the vision model which
// of the 3x3 tiles match the instruction, clicks those tiles (human-like) plus
// Verify, and loops across rounds until the challenge closes / a response token
// exists. It does NOT return a token — completing the challenge in-page lets the
// host site's own success callback finish the flow (e.g. Lever's onSuccess →
// hidden submit).
type HCaptcha struct {
	Vision    vision.Model
	MaxRounds int
	rng       *rand.Rand
}

// hCaptcha standard challenge-iframe layout (fractions of the iframe box):
// prompt header on top, 3x3 grid in the middle, Verify button bottom-right.
// Generous on purpose (cells are ~33% wide), easy to tune from live runs.
var hcLayout = gridLayout{marginX: 0.03, topFrac: 0.20, botFrac: 0.85, verifyXFrac: 0.86, verifyYFrac: 0.94}

const (
	hcRows          = 3
	hcCols          = 3
	hcMaxRoundsHard = 6
)

func (s *HCaptcha) Name() string  { return "hcaptcha" }
func (s *HCaptcha) Priority() int { return 150 }

// CanHandle reports true when the page embeds hCaptcha and a vision model is
// available. The actual open-challenge check happens in Solve.
func (s *HCaptcha) CanHandle(_ context.Context, page autosolver.Page) (bool, error) {
	if s.Vision == nil {
		return false, nil
	}
	html, err := page.HTML()
	if err != nil {
		return false, nil
	}
	l := strings.ToLower(html)
	return strings.Contains(l, "hcaptcha") || strings.Contains(l, "h-captcha"), nil
}

func (s *HCaptcha) Solve(ctx context.Context, page autosolver.Page, executor autosolver.ActionExecutor) (*autosolver.Result, error) {
	result := &autosolver.Result{SolverUsed: "hcaptcha"}
	if s.Vision == nil {
		result.Error = "no vision model configured"
		return result, fmt.Errorf("hcaptcha: no vision model configured")
	}
	if s.rng == nil {
		s.rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	rounds := s.MaxRounds
	if rounds <= 0 || rounds > hcMaxRoundsHard {
		rounds = hcMaxRoundsHard
	}

	for round := 0; round < rounds; round++ {
		if err := ctx.Err(); err != nil {
			result.Error = ctx.Err().Error()
			return result, ctx.Err()
		}
		result.Attempts = round + 1

		// 1) Ensure the challenge is open; if only the checkbox shows, click it.
		b, _ := s.challengeBox(ctx, executor)
		if b == nil {
			if s.openViaCheckbox(ctx, executor) {
				time.Sleep(1500 * time.Millisecond)
				b, _ = s.challengeBox(ctx, executor)
			}
		}
		if b == nil {
			if s.hasResponse(ctx, executor) {
				result.Solved = true
				return result, nil
			}
			result.Error = "no open hCaptcha challenge found"
			return result, nil
		}

		// 2) Screenshot + crop to the challenge for a focused vision input.
		shot, err := page.Screenshot()
		if err != nil {
			return result, fmt.Errorf("hcaptcha: screenshot: %w", err)
		}
		img := cropPNG(shot, *b, devicePixelRatio(ctx, executor))

		// 3) Ask the vision model which tiles match.
		sol, err := s.Vision.SolveGrid(ctx, vision.GridChallenge{
			Image: img, Rows: hcRows, Cols: hcCols, Hint: "hCaptcha image challenge",
		})
		if err != nil {
			return result, fmt.Errorf("hcaptcha: vision: %w", err)
		}

		// 4) Click matching tiles, then Verify/Next.
		var clickPts [][2]float64
		for _, idx := range sol.Tiles {
			if cx, cy, ok := hcLayout.tile(*b, hcRows, hcCols, idx); ok {
				clickPts = append(clickPts, [2]float64{cx, cy})
				humanClick(s.rng, ctx, executor, cx, cy)
			}
		}
		vx, vy := hcLayout.verify(*b)
		dumpSolverDebug(solverDebugDir(), fmt.Sprintf("hcaptcha-r%d", round+1), img, map[string]any{
			"box": *b, "prompt": sol.Prompt, "tiles": sol.Tiles, "done": sol.Done,
			"clickPoints": clickPts, "verify": [2]float64{vx, vy},
		})
		humanClick(s.rng, ctx, executor, vx, vy)

		// 5) Settle, then check success (token present or challenge closed).
		time.Sleep(2200 * time.Millisecond)
		if s.hasResponse(ctx, executor) {
			result.Solved = true
			result.FinalTitle = page.Title()
			result.FinalURL = page.URL()
			return result, nil
		}
		if nb, _ := s.challengeBox(ctx, executor); nb == nil {
			result.Solved = true
			result.FinalTitle = page.Title()
			result.FinalURL = page.URL()
			return result, nil
		}
		// else a new round rendered (wrong answer / multi-round) -> loop.
	}

	result.FinalTitle = page.Title()
	result.Solved = s.hasResponse(ctx, executor)
	return result, nil
}

// challengeBox returns the open hCaptcha challenge iframe box (CSS px) or nil.
func (s *HCaptcha) challengeBox(ctx context.Context, executor autosolver.ActionExecutor) (*box, error) {
	return evalBox(ctx, executor, `(() => {
		let best = null, bestArea = 0;
		for (const f of document.querySelectorAll('iframe')) {
			const src = (f.src || '').toLowerCase();
			if (!src.includes('hcaptcha.com')) continue;
			const r = f.getBoundingClientRect();
			const area = r.width * r.height;
			const isChallenge = src.includes('challenge') || (r.width > 250 && r.height > 350);
			if (isChallenge && area > bestArea && r.width > 50 && r.height > 50) {
				best = {x: r.x, y: r.y, width: r.width, height: r.height}; bestArea = area;
			}
		}
		return best;
	})()`)
}

// openViaCheckbox clicks the hCaptcha checkbox iframe to open the challenge.
func (s *HCaptcha) openViaCheckbox(ctx context.Context, executor autosolver.ActionExecutor) bool {
	b, err := evalBox(ctx, executor, `(() => {
		const f = [...document.querySelectorAll('iframe')].find(f => {
			const src = (f.src || '').toLowerCase();
			return src.includes('hcaptcha.com') && (src.includes('checkbox') || f.getBoundingClientRect().height < 120);
		});
		if (!f) return null;
		const r = f.getBoundingClientRect();
		if (r.width < 20 || r.height < 20) return null;
		return {x: r.x, y: r.y, width: r.width, height: r.height};
	})()`)
	if err != nil || b == nil {
		return false
	}
	return executor.Click(ctx, b.x+b.width*0.12, b.y+b.height*0.5) == nil
}

// hasResponse reports whether hCaptcha produced a response token.
func (s *HCaptcha) hasResponse(ctx context.Context, executor autosolver.ActionExecutor) bool {
	var n float64
	err := executor.Evaluate(ctx, `(() => {
		try {
			if (typeof hcaptcha !== 'undefined' && hcaptcha.getResponse) {
				const r = hcaptcha.getResponse(); return r ? r.length : 0;
			}
			const el = document.querySelector('[name="h-captcha-response"], #h-captcha-response, #hcaptchaResponseInput');
			return (el && el.value) ? el.value.length : 0;
		} catch (e) { return 0; }
	})()`, &n)
	return err == nil && n > 0
}
