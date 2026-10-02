package battletrain

import (
	"fmt"
	"math"
	"reflect"
	"testing"
)

func TestBalancedPromotionKeepsFamilyAsStatisticalUnit(t *testing.T) {
	gate := DefaultPromotionGate()
	legacy := gateGames(256, true, 205)
	want, e := assessPromotion(legacy, gate, 1, true)
	if e != nil {
		t.Fatal(e)
	}
	balanced := legacy
	balanced.Config.Pairing = BalancedPairing
	balanced.Games = append(append([]EvaluationGame(nil), legacy.Games...), legacy.Games...)
	got, e := assessPromotion(balanced, gate, 1, true)
	if e != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("eight repeats inflated independent sample size", got, e)
	}
	balanced.Games = balanced.Games[:len(balanced.Games)-1]
	if _, e = assessPromotion(balanced, gate, 1, true); e == nil {
		t.Fatal("incomplete eight-game family accepted")
	}
}

func gateGames(groups int, champion bool, wins int) EvaluationReport {
	var r EvaluationReport
	names := promotionRules()
	if champion {
		names = append(names, "champion")
	}
	for _, name := range names {
		for group := 0; group < groups; group++ {
			for repeat := 0; repeat < 4; repeat++ {
				winner := 1 - repeat%2
				if group < wins {
					winner = repeat % 2
				}
				r.Games = append(r.Games, EvaluationGame{Opponent: name, Group: fmt.Sprint(group), CandidateSide: repeat % 2, Winner: winner, Terminated: true})
			}
		}
	}
	return r
}

func TestPromotionGroupBoundsAndSequentialBudget(t *testing.T) {
	g := DefaultPromotionGate()
	r := gateGames(256, true, 205) // Synthetic independent families for statistics only.
	a, e := assessPromotion(r, g, 1, true)
	if e != nil || !a.Passed || len(a.Bounds) != 6 || a.Alpha != .025 {
		t.Fatal(a, e)
	}
	for _, b := range a.Bounds {
		if b.Groups != 256 || b.Mean == nil || math.Abs(*b.Mean-205./256) > 1e-12 || b.Lower == nil || *b.Lower >= *b.Mean {
			t.Fatal("incorrect group bound", b)
		}
		want := *b.Mean - math.Sqrt(math.Log(6/.025)/(2*256))
		if math.Abs(*b.Lower-want) > 1e-12 {
			t.Fatal("correlated games counted as independent trials", b)
		}
	}
	later, e := assessPromotion(r, g, 1000, true)
	if e != nil || later.Alpha >= a.Alpha || *later.Bounds[0].Lower >= *a.Bounds[0].Lower {
		t.Fatal("attempt multiplicity ignored", e)
	}
	spent := 0.
	for i := 1; i <= 10000; i++ {
		spent += g.Alpha / (float64(i) * float64(i+1))
	}
	if spent >= g.Alpha {
		t.Fatal("error budget exceeded")
	}
	// All wins still require the declared sample size and have uncertainty.
	small, e := assessPromotion(gateGames(20, false, 20), g, 1, false)
	if e != nil || small.Passed || small.Bounds[0].Lower == nil || *small.Bounds[0].Lower >= 1 {
		t.Fatal("tiny perfect sample promoted", e)
	}
	// One weak required opponent cannot be hidden by a strong pooled score.
	for i := range r.Games {
		if r.Games[i].Opponent == "sustain" {
			r.Games[i].Winner = 1 - r.Games[i].CandidateSide
		}
	}
	weak, e := assessPromotion(r, g, 1, true)
	if e != nil || weak.Passed {
		t.Fatal("weak opponent hidden by pooling", e)
	}
	// A cutoff rejects the gate and has no fabricated completed-score estimate.
	r = gateGames(256, false, 256)
	r.Games[0].Truncated, r.Games[0].Terminated, r.Games[0].Winner = true, false, -1
	cutoff, e := assessPromotion(r, g, 1, false)
	if e != nil || cutoff.Passed {
		t.Fatal("cutoff promoted", e)
	}
	for _, b := range cutoff.Bounds {
		if b.Opponent == "basic" && (b.Mean != nil || b.Lower != nil) {
			t.Fatal("cutoff treated as loss/draw", b)
		}
	}
	if _, e := assessPromotion(gateGames(256, false, 256), g, 1, true); e == nil {
		t.Fatal("missing champion comparison accepted")
	}
	r = gateGames(256, false, 256)
	r.Games = r.Games[1:]
	if _, e := assessPromotion(r, g, 1, false); e == nil {
		t.Fatal("dropped paired game accepted")
	}
}
