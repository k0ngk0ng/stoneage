package arenaagent

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"math/rand"
	"net/url"
	"os"
	"path/filepath"
	"sort"
)

const FeatureVersion = "joint-observed-v1"
const RulesVersion = "stoneage-native-ladder-v1"

func ratio(a, b any) float64 { return max(0, min(1, num(a)/max(1, num(b)))) }
func features(t Object, choices map[Slot]string) map[string]float64 {
	out := map[string]float64{"bias": 1, "mode": num(t["mode"]) / 5}
	participants := map[int]Object{}
	views := obj(t["members"])
	for _, member := range sortedKeys(views) {
		for _, p := range objects(obj(obj(views[member])["battle"])["Participants"]) {
			bid := integer(p["BattleID"])
			if participants[bid] == nil {
				participants[bid] = p
			}
		}
	}
	for side := 0; side < 2; side++ {
		label := "enemy"
		if side == integer(t["side"]) {
			label = "ally"
		}
		hp, alive, n := 0., 0., 0.
		for bid, p := range participants {
			if bid/10 != side {
				continue
			}
			n++
			hp += ratio(p["HP"], p["MaxHP"])
			if num(p["HP"]) > 0 {
				alive++
			}
		}
		out[label+"_hp"] = hp / max(1, n)
		out[label+"_alive"] = alive / max(1, n)
	}
	attacks := map[int]int{}
	available := slots(t)
	scale := float64(max(1, len(available)))
	// Stable order keeps training and inference numerically reproducible.
	for _, key := range sortedSlots(available) {
		id, chosen := choices[key]
		if !chosen {
			continue
		}
		c := available[key][id]
		v := obj(views[key.Member])
		battle := obj(v["battle"])
		bid := integer(battle["MyNo"])
		own := obj(v["own"])
		if key.Actor == "pet" {
			bid += 5
			slot := integer(own["BattlePetSlot"])
			own = nil
			for _, pet := range objects(v["pets"]) {
				if integer(pet["Slot"]) == slot {
					own = pet
					break
				}
			}
		}
		me := participants[bid]
		target := integer(c["target"])
		p := participants[target]
		kind := str(c["kind"])
		prefix := key.Actor + ":" + kind
		out[prefix] += 1 / scale
		out[prefix+":own_hp"] += ratio(me["HP"], me["MaxHP"]) / scale
		for _, stat := range []string{"Attack", "Defense", "Quick", "Vital", "Strength", "Toughness", "Dexterity", "Earth", "Water", "Fire", "Wind"} {
			if value, ok := own[stat]; ok {
				out[prefix+":"+stat] += min(10, max(0, num(value))/100) / scale
				out[prefix+":"+stat+":known"] += 1 / scale
			}
		}
		if key.Actor == "player" {
			out[prefix+":mp"] += ratio(battle["MyMP"], own["MaxMP"]) / scale
		}
		if target >= 0 && target < 20 {
			relation := ":ally"
			if target/10 != integer(t["side"]) {
				relation = ":enemy"
			}
			out[prefix+relation] += 1 / scale
			out[prefix+relation+":target_hp"] += ratio(p["HP"], p["MaxHP"]) / scale
			out[prefix+relation+":target_player"] += 0
			if target%10 < 5 {
				out[prefix+relation+":target_player"] += 1 / scale
			}
			if kind == "attack" || kind == "skill" && integer(c["skill_id"]) == 1 {
				attacks[target]++
			}
		}
		if skill := integer(c["skill_id"]); skill != 0 {
			out[fmt.Sprintf("skill:%d", skill)] += 1 / scale
		}
	}
	for target, count := range attacks {
		label := "ally"
		if target/10 != integer(t["side"]) {
			label = "enemy"
		}
		hp := ratio(participants[target]["HP"], participants[target]["MaxHP"])
		focus := float64(count*(count-1)) / (scale * scale)
		out["focus:"+label] += focus
		out["focus_low_hp:"+label] += focus * (1 - hp)
	}
	return out
}
func score(w, x map[string]float64) float64 {
	v := 0.
	for _, k := range sortedKeys(x) {
		v += w[k] * x[k]
	}
	return v
}
func sigmoid(v float64) float64 { return 1 / (1 + math.Exp(-max(-30, min(30, v)))) }
func actionClass(t, c Object) string {
	target := integer(c["target"])
	relation := "group_or_none"
	if target >= 0 && target < 20 {
		relation = "ally"
		if target/10 != integer(t["side"]) {
			relation = "enemy"
		}
	}
	return fmt.Sprintf("%s:%s:%d:%s", str(c["actor"]), str(c["kind"]), integer(c["skill_id"]), relation)
}

type Learned struct {
	Model   Object
	Weights map[string]float64
	mode    int
	version string
}

func NewLearned(path string, mode int) (*Learned, error) {
	raw, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	var m Object
	if decode(raw, &m) != nil {
		return nil, fmt.Errorf("invalid model JSON")
	}
	if integer(m["schema_version"]) != 1 || str(m["features"]) != FeatureVersion || str(m["rules"]) != RulesVersion {
		return nil, fmt.Errorf("model feature/rules schema is incompatible")
	}
	if !modeSupported(arr(m["modes"]), mode) {
		return nil, fmt.Errorf("model has no training coverage for %dv%d", mode, mode)
	}
	weights := map[string]float64{}
	for k, v := range obj(m["weights"]) {
		n, ok := v.(float64)
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, fmt.Errorf("model weights invalid")
		}
		weights[k] = n
	}
	if len(weights) == 0 || len(arr(m["action_support"])) == 0 {
		return nil, fmt.Errorf("model weights and action support required")
	}
	return &Learned{m, weights, mode, hash(m)}, nil
}
func (l *Learned) ID() string      { return "learned" }
func (l *Learned) Version() string { return l.version }
func (l *Learned) Decide(ctx context.Context, t Object, h []Object) (Decision, error) {
	if integer(t["mode"]) != l.mode || str(t["rules_version"]) != str(l.Model["rules"]) {
		return Decision{}, fmt.Errorf("model mode or server rules mismatch")
	}
	type node struct {
		value   float64
		choices map[Slot]string
	}
	beam := []node{{0, map[Slot]string{}}}
	available := slots(t)
	for _, key := range sortedSlots(available) {
		ids := []string{}
		for _, id := range sortedKeys(available[key]) {
			if contains(arr(l.Model["action_support"]), actionClass(t, available[key][id])) {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			return Decision{}, fmt.Errorf("model has no observed action class for this slot")
		}
		expanded := []node{}
		for _, item := range beam {
			for _, id := range ids {
				if e := ctx.Err(); e != nil {
					return Decision{}, e
				}
				choices := map[Slot]string{}
				for k, v := range item.choices {
					choices[k] = v
				}
				choices[key] = id
				expanded = append(expanded, node{score(l.Weights, features(t, choices)), choices})
			}
		}
		sort.SliceStable(expanded, func(i, j int) bool { return expanded[i].value > expanded[j].value })
		beam = expanded[:min(8, len(expanded))]
	}
	p := planFor(t, beam[0].choices)
	return Decision{p, l.ID(), l.Version(), Object{"estimated_win_probability": sigmoid(beam[0].value), "model_status": l.Model["status"], "beam_width": 8}}, validatePlan(t, p)
}

type example struct {
	X            map[string]float64
	Y            float64
	Support      []string
	Group, Match string
	Mode         int
}

func readObjects(db *sql.DB, query string, args ...any) ([]Object, error) {
	rows, e := db.Query(query, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Object{}
	for rows.Next() {
		var b string
		var v Object
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		if e = decode([]byte(b), &v); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func readExamples(paths []string) ([]example, map[string]int, error) {
	out := []example{}
	excluded := map[string]int{}
	for _, path := range paths {
		rows, e := examplesFrom(path, excluded)
		if e != nil {
			return nil, nil, e
		}
		out = append(out, rows...)
	}
	return out, excluded, nil
}
func examplesFrom(path string, excluded map[string]int) ([]example, error) {
	path, e := filepath.Abs(path)
	if e != nil {
		return nil, e
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	db, e := sql.Open("sqlite", u.String())
	if e != nil {
		return nil, e
	}
	defer db.Close()
	results, e := readObjects(db, "SELECT body FROM results")
	if e != nil {
		return nil, e
	}
	resultByID := map[string]Object{}
	for _, r := range results {
		resultByID[str(r["id"])] = r
	}
	gaps := map[string]bool{}
	q, e := db.Query("SELECT DISTINCT match_id FROM records WHERE kind='event_gap' UNION SELECT DISTINCT match_id FROM battle_intents WHERE status='uncertain'")
	if e != nil {
		return nil, e
	}
	for q.Next() {
		var id string
		if e = q.Scan(&id); e != nil {
			q.Close()
			return nil, e
		}
		gaps[id] = true
	}
	e = q.Err()
	q.Close()
	if e != nil {
		return nil, e
	}
	turns, e := readObjects(db, "SELECT body FROM records WHERE kind='turn' ORDER BY id")
	if e != nil {
		return nil, e
	}
	out := []example{}
	for _, row := range turns {
		t := obj(row["team"])
		match := str(t["match_id"])
		r := resultByID[match]
		if str(t["rules_version"]) != RulesVersion {
			excluded["unknown_or_incompatible_rules"]++
			continue
		}
		if r == nil || str(r["reason"]) != "defeat" || r["winner_side"] == nil || integer(r["winner_side"]) < 0 || integer(r["winner_side"]) > 1 || gaps[match] || len(arr(t["missing_ids"])) != 0 {
			excluded["incomplete_or_noncombat"]++
			continue
		}
		p, e := parsePlan(enc(row["plan"]), t)
		if e != nil {
			return nil, e
		}
		choices := map[Slot]string{}
		valid := len(p.Orders) > 0
		for _, o := range p.Orders {
			var status, body string
			e = db.QueryRow("SELECT status,body FROM battle_intents WHERE member=? AND match_id=? AND turn=? AND actor=?", o.Member, match, p.Turn, o.Actor).Scan(&status, &body)
			if e != nil && e != sql.ErrNoRows {
				return nil, e
			}
			var intent Object
			if e != nil || decode([]byte(body), &intent) != nil || status != "written" || str(intent["candidate_id"]) != o.Candidate {
				valid = false
			}
			choices[Slot{o.Member, o.Actor}] = o.Candidate
		}
		if !valid {
			excluded["unconfirmed_plan"]++
			continue
		}
		roster := []string{}
		for _, m := range objects(r["members"]) {
			roster = append(roster, str(m["id"]))
		}
		sort.Strings(roster)
		support := map[string]bool{}
		available := slots(t)
		for k, v := range choices {
			support[actionClass(t, available[k][v])] = true
		}
		y := 0.
		if integer(r["winner_side"]) == integer(t["side"]) {
			y = 1
		}
		out = append(out, example{features(t, choices), y, sortedKeys(support), hash(Object{"mode": integer(t["mode"]), "roster": roster}), match, integer(t["mode"])})
	}
	return out, nil
}
func metrics(rows []example, w map[string]float64) any {
	if len(rows) == 0 {
		return nil
	}
	loss, correct := 0., 0.
	matches := map[string]bool{}
	for _, r := range rows {
		v := score(w, r.X)
		p := sigmoid(v)
		loss -= r.Y*math.Log(max(1e-12, p)) + (1-r.Y)*math.Log(max(1e-12, 1-p))
		if (v >= 0) == (r.Y == 1) {
			correct++
		}
		matches[r.Match] = true
	}
	return Object{"rows": len(rows), "matches": len(matches), "log_loss": loss / float64(len(rows)), "accuracy": correct / float64(len(rows))}
}
func Train(paths []string, output string, seed int64, epochs int) (Object, error) {
	if epochs < 1 || epochs > 10000 || len(paths) == 0 || output == "" {
		return nil, fmt.Errorf("train requires databases, new output and epochs in 1..10000")
	}
	rows, excluded, e := readExamples(paths)
	if e != nil {
		return nil, e
	}
	labels := map[float64]bool{}
	groups := map[string]bool{}
	modes := map[int]bool{}
	for _, r := range rows {
		labels[r.Y] = true
		groups[r.Group] = true
		modes[r.Mode] = true
	}
	if len(rows) < 4 || len(labels) < 2 {
		return nil, fmt.Errorf("need at least four confirmed plans with winning and losing outcomes")
	}
	rng := rand.New(rand.NewSource(seed))
	ordered := sortedKeys(groups)
	rng.Shuffle(len(ordered), func(i, j int) { ordered[i], ordered[j] = ordered[j], ordered[i] })
	held := map[string]bool{}
	for mode := range modes {
		candidates := []string{}
		for _, g := range ordered {
			for _, r := range rows {
				if r.Group == g && r.Mode == mode {
					candidates = append(candidates, g)
					break
				}
			}
		}
		if len(candidates) > 1 {
			for _, g := range candidates[:max(1, len(candidates)/5)] {
				held[g] = true
			}
		}
	}
	training, testing := []example{}, []example{}
	for _, r := range rows {
		if held[r.Group] {
			testing = append(testing, r)
		} else {
			training = append(training, r)
		}
	}
	weights := map[string]float64{}
	counts := map[string]int{}
	support := map[string]bool{}
	trainGroups := map[string]bool{}
	trainedModes := map[int]bool{}
	for _, r := range training {
		counts[r.Match]++
		trainGroups[r.Group] = true
		trainedModes[r.Mode] = true
		for _, s := range r.Support {
			support[s] = true
		}
	}
	for epoch := 0; epoch < epochs; epoch++ {
		rng.Shuffle(len(training), func(i, j int) { training[i], training[j] = training[j], training[i] })
		for _, r := range training {
			errorValue := r.Y - sigmoid(score(weights, r.X))
			rate := .15 / (1 + float64(epoch)*.03) / float64(counts[r.Match])
			for _, k := range sortedKeys(r.X) {
				weights[k] += rate * (errorValue*r.X[k] - .001*weights[k])
			}
		}
	}
	modeList := []int{}
	for mode := range trainedModes {
		modeList = append(modeList, mode)
	}
	sort.Ints(modeList)
	report := Object{"seed": seed, "epochs": epochs, "groups": len(groups), "excluded": excluded, "training_groups": sortedKeys(trainGroups), "evaluation_groups": sortedKeys(held), "train": metrics(training, weights), "held_out": metrics(testing, weights), "held_out_baseline": metrics(testing, nil)}
	model := Object{"schema_version": 1, "features": FeatureVersion, "rules": RulesVersion, "status": "experimental", "algorithm": "joint-linear-outcome-v1", "modes": modeList, "action_support": sortedKeys(support), "weights": weights, "training": report, "limitations": []string{"Outcome correlation is not a causal action value.", "No claim of win-rate improvement without native paired-policy evaluation.", "Only configured modes and observed loadouts have data coverage."}}
	if e = os.MkdirAll(filepath.Dir(output), 0700); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return nil, e
	}
	_, e = f.Write(append(enc(model), '\n'))
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return nil, e
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return Object{"model": output, "version": hash(model), "training": report, "modes": modeList}, nil
}
func Evaluate(paths []string, path string) (Object, error) {
	raw, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	var m Object
	if e = decode(raw, &m); e != nil {
		return nil, e
	}
	if len(arr(m["modes"])) == 0 {
		return nil, fmt.Errorf("model has no modes")
	}
	var learned *Learned
	for _, mode := range arr(m["modes"]) {
		learned, e = NewLearned(path, integer(mode))
		if e != nil {
			return nil, e
		}
	}
	rows, excluded, e := readExamples(paths)
	if e != nil {
		return nil, e
	}
	independent := []example{}
	overlap := 0
	used := arr(obj(m["training"])["training_groups"])
	for _, r := range rows {
		if contains(used, r.Group) {
			overlap++
			continue
		}
		if modeSupported(arr(m["modes"]), r.Mode) {
			independent = append(independent, r)
		}
	}
	if len(independent) == 0 {
		return nil, fmt.Errorf("no independent evaluation matches; collect new roster groups")
	}
	return Object{"model_version": hash(m), "excluded": excluded, "overlapping_rows": overlap, "value_prediction": metrics(independent, learned.Weights), "constant_baseline": metrics(independent, nil), "note": "Prediction quality is not policy win rate; use native paired-policy matches."}, nil
}
