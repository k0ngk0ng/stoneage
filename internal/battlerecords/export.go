package battlerecords

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
)

type Config struct {
	Root, Output, Format, Mode   string
	EqualPoints, IncludeAbnormal bool
}

type Report struct {
	Event           string         `json:"event"`
	Schema          string         `json:"schema"`
	Output          string         `json:"output"`
	SHA256          string         `json:"sha256"`
	Bytes           int64          `json:"bytes"`
	MatchesSeen     int            `json:"matches_seen"`
	ExportedMatches int            `json:"exported_matches"`
	FilteredMatches int            `json:"filtered_matches"`
	InvalidMatches  int            `json:"invalid_or_incomplete"`
	Rows            int            `json:"rows"`
	InvalidReasons  map[string]int `json:"invalid_reasons"`
	OnPolicyPPO     bool           `json:"on_policy_ppo"`
}

// Export validates each entire match before emitting any of its rows. It
// publishes a complete private JSONL file atomically without replacing an
// existing destination. Cancellation or an empty result publishes nothing.
func Export(ctx context.Context, c Config) (Report, error) {
	r := Report{Event: "records_exported", Schema: "battle-record-export-v1", Output: c.Output, InvalidReasons: map[string]int{}}
	if c.Format != "builds" && c.Format != "transitions" || c.Mode != "all" && c.Mode != "pvp" && c.Mode != "pve" && c.Mode != "pvp-1v1" || c.Root == "" || c.Output == "" {
		return r, fmt.Errorf("export requires records, output, format builds/transitions and mode all/pvp/pve/pvp-1v1")
	}
	if err := ctx.Err(); err != nil {
		return r, err
	}
	info, err := os.Stat(c.Root)
	if err != nil || !info.IsDir() {
		return r, fmt.Errorf("record root must be an existing directory")
	}
	if _, err := os.Lstat(c.Output); !os.IsNotExist(err) {
		return r, fmt.Errorf("output must be a new file")
	}
	var paths []string
	if _, err := os.Stat(filepath.Join(c.Root, "metadata.json")); err == nil {
		paths = []string{filepath.Join(c.Root, "metadata.json")}
	} else {
		paths, err = filepath.Glob(filepath.Join(c.Root, "????-??-??", "*", "metadata.json"))
		if err != nil {
			return r, err
		}
	}
	sort.Strings(paths)
	f, err := os.CreateTemp(filepath.Dir(c.Output), ".records-export-*")
	if err != nil {
		return r, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	h := sha256.New()
	encoder := json.NewEncoder(io.MultiWriter(f, h))
	encoder.SetEscapeHTML(false)
	seen := map[string]bool{}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return r, err
		}
		r.MatchesSeen++
		m, err := load(ctx, filepath.Dir(path))
		if err != nil {
			if ctx.Err() != nil {
				return r, ctx.Err()
			}
			r.InvalidMatches++
			r.InvalidReasons[failureReason(err)]++
			continue
		}
		id := text(m.metadata["match_id"])
		if seen[id] {
			// Do not give copied battles extra training weight. Refuse the
			// entire export rather than select an arbitrary conflicting copy.
			return r, fmt.Errorf("duplicate match identity in record root")
		}
		seen[id] = true
		rows, err := m.export(c)
		if err != nil {
			r.InvalidMatches++
			r.InvalidReasons[failureReason(err)]++
			continue
		}
		if len(rows) == 0 {
			r.FilteredMatches++
			continue
		}
		for _, row := range rows {
			if err := ctx.Err(); err != nil {
				return r, err
			}
			if err := encoder.Encode(row); err != nil {
				return r, err
			}
			r.Rows++
		}
		r.ExportedMatches++
	}
	if r.Rows == 0 {
		return r, fmt.Errorf("no eligible records: seen=%d filtered=%d invalid=%d", r.MatchesSeen, r.FilteredMatches, r.InvalidMatches)
	}
	if err := f.Sync(); err != nil {
		return r, err
	}
	info, err = f.Stat()
	if err != nil {
		return r, err
	}
	r.Bytes = info.Size()
	r.SHA256 = hex.EncodeToString(h.Sum(nil))
	if err := f.Close(); err != nil {
		return r, err
	}
	if err := ctx.Err(); err != nil {
		return r, err
	}
	if err := os.Link(f.Name(), c.Output); err != nil {
		return r, err
	}
	d, err := os.Open(filepath.Dir(c.Output))
	if err != nil {
		return r, err
	}
	defer d.Close()
	return r, d.Sync()
}

func fairness(initial []*observation) (object, error) {
	f := object{"equal_points": false, "controlled_context": false}
	if len(initial) != 2 || initial[0].side != 0 || initial[1].side != 1 {
		return f, nil
	}
	budgets, controls := []int64{}, []object{}
	for _, o := range initial {
		a, err := ownRow(o.row, "allocations")
		if err != nil {
			return nil, err
		}
		b, err := budget(a)
		if err != nil {
			return nil, err
		}
		budgets = append(budgets, b)
		own, err := ownRow(o.row, "own_actors")
		if err != nil {
			return nil, err
		}
		c, err := selectFields(a, "level rebirths charm luck base_elements unspent_points")
		if err != nil {
			return nil, err
		}
		c["active_pet_slot"], c["ride_pet_slot"] = own["active_pet_slot"], own["ride_pet_slot"]
		c["full_health"] = integer(own["hp"]) >= 0 && integer(own["mp"]) >= 0 && own["hp"] == own["max_hp"] && own["mp"] == own["max_mp"]
		for field, output := range map[string]string{"own_actors": "pets", "allocations": "pet_allocations"} {
			rows := []object{}
			for _, row := range o.row[field].([]object) {
				if integer(row["pet_slot"]) >= 0 {
					rows = append(rows, row)
				}
			}
			c[output] = rows
		}
		c["pet_skills"], c["items"] = o.row["pet_skills"], o.row["items"]
		statuses := []object{}
		for _, actor := range o.row["actors"].([]object) {
			if integer(actor["bid"]) == o.bid {
				s, err := selectFields(actor, "flags ride_flag")
				if err != nil {
					return nil, err
				}
				statuses = append(statuses, s)
			}
		}
		if len(statuses) != 1 {
			return nil, invalid("missing_or_duplicate_visible_self")
		}
		c["visible_status"] = statuses
		controls = append(controls, c)
	}
	f["equal_points"] = budgets[0] == budgets[1]
	f["controlled_context"] = reflect.DeepEqual(controls[0], controls[1]) && controls[0]["full_health"] == true && integer(controls[0]["unspent_points"]) == 0 && integer(controls[1]["unspent_points"]) == 0
	f["budget_raw"], f["controls"], f["experimental_control_verified"] = budgets, controls, false
	return f, nil
}

func (m *match) common() (object, error) {
	base, err := selectFields(m.metadata, "schema_version match_id mode ruleset_id release experiment")
	if err != nil {
		return nil, err
	}
	group := text(m.metadata["match_id"])
	if m.metadata["experiment"] != nil {
		experiment, ok := m.metadata["experiment"].(map[string]any)
		if !ok || integer(experiment["run_seed"]) < 0 || integer(experiment["pair_index"]) < 0 {
			return nil, invalid("invalid_experiment")
		}
		// Keep the original grouping so swapping/repeating a legacy fixture
		// cannot move it across splits merely by using the new Go exporter.
		group = fmt.Sprintf("%s:%d:%d", text(m.metadata["ruleset_id"]), integer(experiment["run_seed"]), integer(experiment["pair_index"]))
		exported, err := selectFields(experiment, "generator run_seed combat_seed match_index pair_index repetition side_swap points level scenario allocation_generator policy_ids max_turns")
		if err != nil {
			return nil, err
		}
		base["experiment"] = exported
	}
	h := sha256.Sum256([]byte(group))
	bucket := binary.BigEndian.Uint32(h[:4]) % 100
	split := "train"
	if bucket >= 90 {
		split = "test"
	} else if bucket >= 80 {
		split = "validation"
	}
	base["split_group"], base["suggested_split"] = group, split
	base["winner_side"], base["end_reason"] = m.result["winner_side"], m.result["end_reason"]
	return base, nil
}

func clone(row object) object {
	out := object{}
	for k, v := range row {
		out[k] = v
	}
	return out
}

func (m *match) export(c Config) ([]object, error) {
	mode := text(m.metadata["mode"])
	if c.Mode != "all" && c.Mode != mode && !(c.Mode == "pvp-1v1" && mode == "pvp") {
		return nil, nil
	}
	counts := m.metadata["players_per_side"].([]any)
	one := integer(counts[0]) == 1 && integer(counts[1]) == 1
	if c.Mode == "pvp-1v1" && !one {
		return nil, nil
	}
	reason := text(m.result["end_reason"])
	if reason != "defeat" && reason != "turn_limit" && !c.IncludeAbnormal {
		return nil, nil
	}
	byPlayer := map[int64][]*observation{}
	for _, o := range m.observations {
		byPlayer[o.bid] = append(byPlayer[o.bid], o)
	}
	var bids []int64
	for bid := range byPlayer {
		bids = append(bids, bid)
	}
	sort.Slice(bids, func(i, j int) bool { return bids[i] < bids[j] })
	if len(bids) != len(m.players) {
		return nil, invalid("missing_player_observation")
	}
	initial := []*observation{}
	for _, bid := range bids {
		states := byPlayer[bid]
		if states[0].turn != 0 {
			return nil, invalid("missing_initial_observation")
		}
		for i := 1; i < len(states); i++ {
			if states[i].turn != states[i-1].turn+1 {
				return nil, invalid("nonconsecutive_decision_states")
			}
		}
		initial = append(initial, states[0])
	}
	fair, err := fairness(initial)
	if err != nil {
		return nil, err
	}
	if c.EqualPoints && (mode != "pvp" || fair["equal_points"] != true) {
		return nil, nil
	}
	base, err := m.common()
	if err != nil {
		return nil, err
	}
	if c.Format == "builds" {
		if mode != "pvp" || !one || fair["equal_points"] != true || fair["controlled_context"] != true {
			return nil, nil
		}
		builds := []object{}
		for _, o := range initial {
			a, err := ownRow(o.row, "allocations")
			if err != nil {
				return nil, err
			}
			builds = append(builds, object{"side": o.row["side"], "allocation": a, "initial_observation": o.row})
		}
		base["fairness"], base["builds"], base["rounds"] = fair, builds, m.result["turn"]
		base["outcome"] = "win_loss"
		if integer(m.result["winner_side"]) < 0 {
			base["outcome"] = "censored"
		}
		return []object{base}, nil
	}
	rows := []object{}
	for _, bid := range bids {
		states := byPlayer[bid]
		for i := 0; i+1 < len(states); i++ {
			o, next := states[i], states[i+1]
			terminal, winner := next.turn == integer(m.result["turn"]), integer(m.result["winner_side"])
			reward := 0
			if terminal && winner >= 0 {
				reward = -1
				if winner == o.side {
					reward = 1
				}
			}
			row := clone(base)
			row["actor_bid"], row["side"], row["turn"] = bid, o.side, o.turn
			row["observation"], row["submitted_actions"], row["next_observation"] = o.row, o.requests, next.row
			commands := append([]object{}, m.commands[[2]int64{o.turn, bid}]...)
			row["resolution_commands"] = append(commands, m.commands[[2]int64{o.turn, bid + 5}]...)
			row["resolution_order"] = append([]object{}, m.resolution[o.turn]...)
			row["reward"], row["terminated"], row["truncated"] = reward, terminal && winner >= 0, terminal && winner < 0
			row["policy_version"] = m.players[bid]["policy_version"]
			rows = append(rows, row)
		}
	}
	return rows, nil
}
