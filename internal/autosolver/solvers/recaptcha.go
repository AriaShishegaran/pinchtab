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

// ReCaptcha solves reCAPTCHA v2 image-grid challenges with a vision model, using
// the same screenshot → identify tiles → click → Verify loop as the hCaptcha
// solver. reCAPTCHA v3 (and enterprise score-only) has no clickable challenge
// and is not handled here — those are mitigated by stealth, not solved by clicks.
type ReCaptcha struct {
	Vision    vision.Model
	MaxRounds int
	rng       *rand.Rand
}

// reCAPTCHA bframe layout (fractions of the iframe box): a taller prompt header,
// the 3x3 grid, then the Verify button bottom-right.
var rcLayout = gridLayout{marginX: 0.04, topFrac: 0.24, botFrac: 0.84, verifyXFrac: 0.86, verifyYFrac: 0.95}

const (
	rcRows          = 3
	rcCols          = 3
	rcMaxRoundsHard = 6
)

func (s *ReCaptcha) Name() string  { return "recaptcha" }
func (s *ReCaptcha) Priority() int { return 160 }

func (s *ReCaptcha) CanHandle(_ context.Context, page autosolver.Page) (bool, error) {
	if s.Vision == nil {
		return false, nil
	}
	html, err := page.HTML()
	if err != nil {
		return false, nil
	}
	l := strings.ToLower(html)
	// Only the interactive v2 path: needs the api.js widget, not the v3
	// render=<key> score script (which has no clickable challenge).
	if strings.Contains(l, "recaptcha/api.js?render=") && !strings.Contains(l, "g-recaptcha") {
		return false, nil
	}
	return strings.Contains(l, "g-recaptcha") || strings.Contains(l, "recaptcha"), nil
}

func (s *ReCaptcha) Solve(ctx context.Context, page autosolver.Page, executor autosolver.ActionExecutor) (*autosolver.Result, error) {
	result := &autosolver.Result{SolverUsed: "recaptcha"}
	if s.Vision == nil {
		result.Error = "no vision model configured"
		return result, fmt.Errorf("recaptcha: no vision model configured")
	}
	if s.rng == nil {
		s.rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	rounds := s.MaxRounds
	if rounds <= 0 || rounds > rcMaxRoundsHard {
		rounds = rcMaxRoundsHard
	}

	for round := 0; round < rounds; round++ {
		if err := ctx.Err(); err != nil {
			result.Error = ctx.Err().Error()
			return result, ctx.Err()
		}
		result.Attempts = round + 1

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
			result.Error = "no open reCAPTCHA challenge found"
			return result, nil
		}

		shot, err := page.Screenshot()
		if err != nil {
			return result, fmt.Errorf("recaptcha: screenshot: %w", err)
		}
		img := cropPNG(shot, *b, devicePixelRatio(ctx, executor))

		sol, err := s.Vision.SolveGrid(ctx, vision.GridChallenge{
			Image: img, Rows: rcRows, Cols: rcCols, Hint: "reCAPTCHA image challenge",
		})
		if err != nil {
			return result, fmt.Errorf("recaptcha: vision: %w", err)
		}

		for _, idx := range sol.Tiles {
			if cx, cy, ok := rcLayout.tile(*b, rcRows, rcCols, idx); ok {
				humanClick(s.rng, ctx, executor, cx, cy)
			}
		}
		vx, vy := rcLayout.verify(*b)
		humanClick(s.rng, ctx, executor, vx, vy)

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
	}

	result.FinalTitle = page.Title()
	result.Solved = s.hasResponse(ctx, executor)
	return result, nil
}

// challengeBox returns the open reCAPTCHA bframe (image-grid) box or nil.
func (s *ReCaptcha) challengeBox(ctx context.Context, executor autosolver.ActionExecutor) (*box, error) {
	return evalBox(ctx, executor, `(() => {
		let best = null, bestArea = 0;
		for (const f of document.querySelectorAll('iframe')) {
			const src = (f.src || '').toLowerCase();
			const title = (f.title || '').toLowerCase();
			const isB = src.includes('recaptcha') && (src.includes('bframe') || title.includes('challenge'));
			if (!isB) continue;
			const r = f.getBoundingClientRect();
			const area = r.width * r.height;
			if (area > bestArea && r.width > 50 && r.height > 50) {
				best = {x: r.x, y: r.y, width: r.width, height: r.height}; bestArea = area;
			}
		}
		return best;
	})()`)
}

// openViaCheckbox clicks the reCAPTCHA anchor checkbox to open the challenge.
func (s *ReCaptcha) openViaCheckbox(ctx context.Context, executor autosolver.ActionExecutor) bool {
	b, err := evalBox(ctx, executor, `(() => {
		const f = [...document.querySelectorAll('iframe')].find(f => {
			const src = (f.src || '').toLowerCase();
			const title = (f.title || '').toLowerCase();
			return src.includes('recaptcha') && (src.includes('anchor') || title === 'recaptcha');
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

// hasResponse reports whether reCAPTCHA produced a response token.
func (s *ReCaptcha) hasResponse(ctx context.Context, executor autosolver.ActionExecutor) bool {
	var n float64
	err := executor.Evaluate(ctx, `(() => {
		try {
			if (typeof grecaptcha !== 'undefined' && grecaptcha.getResponse) {
				const r = grecaptcha.getResponse(); return r ? r.length : 0;
			}
			const el = document.querySelector('[name="g-recaptcha-response"], #g-recaptcha-response');
			return (el && el.value) ? el.value.length : 0;
		} catch (e) { return 0; }
	})()`, &n)
	return err == nil && n > 0
}
