package battlepolicy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlenet"
)

func fixture(mode, side int) []aigame.BattleView {
	var roster []aigame.BattleParticipant
	for s := 0; s < 2; s++ {
		for i := 0; i < mode; i++ {
			roster = append(roster, aigame.BattleParticipant{BattleID: int32(s*10 + i), Name: fmt.Sprint("p", i), Graphic: 100000, Level: 35, HP: 100, MaxHP: 120, Player: true})
			roster = append(roster, aigame.BattleParticipant{BattleID: int32(s*10 + i + 5), Name: fmt.Sprint("pet", i), Graphic: 100266, Level: 35, HP: 70, MaxHP: 90})
		}
	}
	var views []aigame.BattleView
	for i := 0; i < mode; i++ {
		s := aigame.Snapshot{Phase: aigame.PhaseBattle, Player: aigame.PlayerSnapshot{CombatStatsKnown: true, MaxMP: 100, Attack: 70, Defense: 50, Quick: 30, Vital: 30, Strength: 30, Toughness: 30, Dexterity: 30, Earth: 100, BattlePetSlotKnown: true, BattlePetSlot: 0},
			Battle: aigame.BattleSnapshot{Active: true, LadderID: "match", Turn: 2, MyNo: int32(side*10 + i), MyNoKnown: true, BPReceived: true, BCReceived: true, MyMP: 80, Participants: append([]aigame.BattleParticipant(nil), roster...)},
			Pets:   []aigame.PetSnapshot{{Slot: 0, Name: fmt.Sprint("pet", i), Graphic: 100266, HP: 70, MaxHP: 90, MP: 30, MaxMP: 40, Attack: 50, Defense: 40, Quick: 20, CombatStatsKnown: true, UseFlag: 1, Skills: []aigame.PetSkillSnapshot{{Index: 0, ID: 1, Field: 0, Target: 0}, {Index: 1, ID: 2, Field: 0, Target: 2}, {Index: 2, ID: 3, Field: 0, Target: 0}}}},
		}
		v := aigame.NewBattleView(s)
		// Keep a genuinely unsupported spell in the observation; recall is a
		// supported action in v6 and can no longer serve as the mask fixture.
		v.Candidates = append(v.Candidates, aigame.BattleCandidate{ID: "unsupported-spell", Actor: "player", Kind: "magic", MagicIDKnown: true, MagicID: 999, Target: s.Battle.MyNo})
		v.Mode = mode
		views = append(views, v)
	}
	return views
}

func TestFeatureVisibilityAndRouting(t *testing.T) {
	views := fixture(2, 0)
	f, e := Encode(views, History{First: true})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.Validate(); e != nil {
		t.Fatal(e)
	}
	if len(f.Entities) != 8 || len(f.Slots) != 4 || f.Excluded == 0 {
		t.Fatalf("missing team/pet/coverage data: %+v", f)
	}
	for _, x := range f.Entities {
		if x[eAlly] == 0 && (x[eStatsKnown] != 0 || x[eMPKnown] != 0 || x[eBuildKnown] != 0 || x[eAttack] != 0) {
			t.Fatal("enemy private attributes leaked")
		}
		if x[eAlly] == 1 && x[eStatsKnown] != 1 {
			t.Fatal("own-team attributes missing")
		}
	}
	views[0].Own.CombatStatsKnown = false
	views[0].Own.Attack = 99999
	missing, e := Encode(views, History{First: true})
	if e != nil {
		t.Fatal(e)
	}
	if missing.Entities[0][eAttack] != 0 || missing.Entities[0][eStatsKnown] != 0 {
		t.Fatal("unknown private attribute presented as observed")
	}
	choices := make([]int, len(f.Slots))
	for i, s := range f.Slots {
		for j, c := range s.Candidates {
			if c.Supported {
				choices[i] = j
				break
			}
		}
	}
	selected, e := f.Selections(choices)
	if e != nil {
		t.Fatal(e)
	}
	if len(selected) != 2 || len(selected[0]) != 2 || selected[0][0].ObservationID != f.Slots[0].Observation {
		t.Fatal("joint plan routing lost")
	}
	for _, mutate := range []func([]aigame.BattleView){
		func(v []aigame.BattleView) { v[1].Turn++ },
		func(v []aigame.BattleView) { v[1].Battle.Participants[0].HP-- },
		func(v []aigame.BattleView) { v[1].Battle.MyNo = v[0].Battle.MyNo },
		func(v []aigame.BattleView) { v[1].Battle.PlayerSubmitted = true },
		func(v []aigame.BattleView) { v[1].Battle.Clock.RulesVersion = "different" },
	} {
		v := fixture(2, 0)
		mutate(v)
		if _, e := Encode(v, History{}); e == nil {
			t.Fatal("inconsistent team accepted")
		}
	}
}

func TestSideAndMemberOrderInvariance(t *testing.T) {
	a, e := Encode(fixture(2, 0), History{First: true})
	if e != nil {
		t.Fatal(e)
	}
	v := fixture(2, 1)
	v[0], v[1] = v[1], v[0]
	b, e := Encode(v, History{First: true})
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(a.Entities, b.Entities) {
		t.Fatal("side identity entered entity features")
	}
	for i, s := range a.Slots {
		for j, c := range s.Candidates {
			d := b.Slots[i].Candidates[j]
			if c.Features != d.Features || c.Target != d.Target || c.Supported != d.Supported {
				t.Fatal("side identity entered action features")
			}
		}
	}
	m, _ := battlenet.NewModel[float32](NetworkConfig(), 17)
	x, e := Forward(context.Background(), m.Bind(&battlenet.Graph[float32]{}), a, nil, nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	y, e := Forward(context.Background(), m.Bind(&battlenet.Graph[float32]{}), b, nil, nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(x.Choices, y.Choices) || x.LogProb.Data[0] != y.LogProb.Data[0] {
		t.Fatal("side/order changed equivalent decisions")
	}
}

func TestHistoryCutoffAndDeduplication(t *testing.T) {
	v := fixture(1, 0)[0]
	delta := 7
	recipient := 10
	batch := aigame.BattleEventBatch{Stream: "stream", Cursor: 2, Observation: v, Events: []aigame.BattleEvent{
		{Sequence: 1, MatchID: v.MatchID, Effects: []aigame.BattleLogEntry{{Kind: "attack", Actor: 0, Target: 10, Damage: 25, Recipient: &recipient}}},
		{Sequence: 2, MatchID: v.MatchID, Effects: []aigame.BattleLogEntry{{Kind: "BD", Target: 0, Resource: "hp", Delta: &delta}}},
	}}
	h := History{Batch: &batch, First: true}
	x, e := EncodeHistory(v, h)
	if e != nil {
		t.Fatal(e)
	}
	if x[0] <= 0 || x[4] <= 0 || x[5] >= 0 || x[12] != 0 || x[13] != 1 {
		t.Fatal("wrong battle event features", x)
	}
	h.First = false
	h.PreviousStream = "stream"
	h.PreviousCursor = 2
	if _, e = EncodeHistory(v, h); e == nil {
		t.Fatal("duplicate event batch accepted")
	}
	batch.Events = nil
	x, e = EncodeHistory(v, h)
	empty := [EventFeatures]float32{}
	empty[127] = 1
	if e != nil || x != empty {
		t.Fatal("empty poll repeated effects", x, e)
	}
	batch.Observation.ID = "other"
	if _, e = EncodeHistory(v, h); e == nil {
		t.Fatal("future observation/history mix accepted")
	}
	batch.Observation = v
	batch.Gap = true
	x, e = EncodeHistory(v, h)
	if e != nil || x[12] != 1 {
		t.Fatal("history gap hidden", x, e)
	}
}

func TestInitialHistoryCursorRequiresObservedMatchStart(t *testing.T) {
	v := fixture(1, 0)[0]
	v.Turn = 0
	batch := aigame.BattleEventBatch{Stream: "stream", Cursor: 51, Observation: v, Events: []aigame.BattleEvent{
		{Sequence: 50, MatchID: v.MatchID, Function: "EN", Integers: []int32{1, 0}},
		{Sequence: 51, MatchID: v.MatchID, Function: "B"},
	}}
	h := History{Batch: &batch, First: true, InitialCursor: 49}
	x, e := EncodeHistory(v, h)
	if e != nil || x[12] != 0 || x[13] != 1 {
		t.Fatal("complete new match in existing stream treated as gap", x, e)
	}
	for _, kind := range []string{"later_turn", "not_first", "wrong_boundary", "missing_EN", "other_match"} {
		copyBatch := batch
		copyBatch.Events = append([]aigame.BattleEvent(nil), batch.Events...)
		copyHistory := h
		copyHistory.Batch = &copyBatch
		observer := v
		switch kind {
		case "later_turn":
			observer.Turn = 1
			copyBatch.Observation = observer
		case "not_first":
			copyHistory.First = false
		case "wrong_boundary":
			copyHistory.InitialCursor = 48
		case "missing_EN":
			copyBatch.Events[0].Function = "B"
		case "other_match":
			copyBatch.Events[0].MatchID = "other"
		}
		if _, e = EncodeHistory(observer, copyHistory); e == nil {
			t.Fatal("unproven history boundary accepted", kind)
		}
	}
}

func TestJointLikelihoodAndRecurrentGradient(t *testing.T) {
	f, e := Encode(fixture(2, 0), History{First: true})
	if e != nil {
		t.Fatal(e)
	}
	c := NetworkConfig()
	c.Width = 8
	c.Heads = 2
	c.Layers = 1
	m, _ := battlenet.NewModel[float32](c, 11)
	g := &battlenet.Graph[float32]{Train: true}
	b := m.Bind(g)
	o, e := Forward(context.Background(), b, f, nil, nil, rand.New(rand.NewSource(7)))
	if e != nil {
		t.Fatal(e)
	}
	sum := float32(0)
	for _, p := range o.ConditionalLogProbs {
		sum += p
	}
	if sum != o.LogProb.Data[0] {
		t.Fatal("not a joint likelihood")
	}
	recomputed, e := Forward(context.Background(), m.Bind(&battlenet.Graph[float32]{}), f, nil, o.Choices, nil)
	if e != nil {
		t.Fatal(e)
	}
	if o.LogProb.Data[0] != recomputed.LogProb.Data[0] {
		t.Fatal("forced trajectory probability differs from sampling")
	}
	second, e := Forward(context.Background(), b, f, o.Memory, o.Choices, nil)
	if e != nil {
		t.Fatal(e)
	}
	loss, e := battlenet.PPOLoss(g, second.LogProb, float64(second.LogProb.Data[0]), 1, .2)
	if e != nil {
		t.Fatal(e)
	}
	g.Backward(loss)
	grad := b.Gradients()
	for _, name := range []string{"history.gates.w", "plan.context.w", "plan.gates.w", "block.0.q.w", "entity.0.w"} {
		norm := 0.
		for _, v := range grad[name] {
			if !finite(v) {
				t.Fatal("nonfinite gradient")
			}
			norm += math.Abs(float64(v))
		}
		if norm <= 1e-10 {
			t.Fatal("missing joint/recurrent gradient", name)
		}
	}
	forced := append([]int(nil), o.Choices...)
	for j, c := range f.Slots[0].Candidates {
		if !c.Supported {
			forced[0] = j
			break
		}
	}
	if _, e = Forward(context.Background(), b, f, nil, forced, nil); e == nil {
		t.Fatal("unsupported action accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = Forward(ctx, b, f, nil, nil, nil); e == nil {
		t.Fatal("cancelled inference ran")
	}
}

// Random initialized weights prove end-to-end observation -> joint policy ->
// shared candidate resolution -> real native execution, NOT playing strength.
func TestNativePolicyRollout(t *testing.T) {
	raw := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit native engine command required")
	}
	var command []string
	if e := json.Unmarshal([]byte(raw), &command); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	engine, e := battleenv.Start(ctx, command, io.Discard)
	if e != nil {
		t.Fatal(e)
	}
	defer engine.Close()
	c := NetworkConfig()
	c.Width = 16
	c.Heads = 2
	c.Layers = 1
	m, _ := battlenet.NewModel[float32](c, 19)
	rng := rand.New(rand.NewSource(29))
	for _, mode := range []int{1, 2, 3, 4, 5} {
		scenario := battleenv.Scenario{Seed: 37, Mode: mode, Level: 35, MaxTurns: 25}
		for i := 0; i < 2*mode; i++ {
			scenario.Builds = append(scenario.Builds, battleenv.Build{30, 30, 30, 30})
			scenario.PetBuilds = append(scenario.PetBuilds, battleenv.Build{30, 30, 30, 30})
		}
		state, e := engine.Reset(ctx, scenario)
		if e != nil {
			t.Fatal(e)
		}
		var streams [2]string
		var cursors [2]uint64
		var memory [2][]float32
		effects := 0
		for !state.Terminated && !state.Truncated {
			var joint [][]aigame.BattleSelection
			for side := 0; side < 2; side++ {
				i := side * mode
				for _, event := range state.Events[i].Events {
					effects += len(event.Effects)
				}
				h := History{Batch: &state.Events[i], PreviousStream: streams[side], PreviousCursor: cursors[side], First: state.Turn == 0}
				f, e := Encode(state.Views[i:i+mode], h)
				if e != nil {
					t.Fatalf("mode=%d turn=%d side=%d: %v", mode, state.Turn, side, e)
				}
				if f.Events[12] != 0 {
					t.Fatal("unexpected native history gap")
				}
				g := &battlenet.Graph[float32]{}
				o, e := Forward(ctx, m.Bind(g), f, g.New(1, c.Width, memory[side]), nil, rng)
				if e != nil {
					t.Fatal(e)
				}
				memory[side] = append([]float32(nil), o.Memory.Data...)
				streams[side], cursors[side] = state.Events[i].Stream, state.Events[i].Cursor
				choices, e := f.Selections(o.Choices)
				if e != nil {
					t.Fatal(e)
				}
				joint = append(joint, choices...)
			}
			state, e = engine.Advance(ctx, joint)
			if e != nil {
				t.Fatalf("mode=%d: %v", mode, e)
			}
		}
		if effects == 0 {
			t.Fatal("native rollout lost structured battle effects")
		}
		t.Logf("mode=%d turns=%d terminated=%v truncated=%v winner=%d", mode, state.Turn, state.Terminated, state.Truncated, state.Winner)
	}
}
