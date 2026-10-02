// Package battlerecords reads the original server battle-record format. These
// observations and outcome labels are not on-policy PPO trajectories.
package battlerecords

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type object map[string]any

type invalid string

func (e invalid) Error() string { return string(e) }

const maxMatchBytes = 64 << 20

func integer(v any) int64 {
	n, ok := v.(json.Number)
	if !ok {
		return -1 << 63
	}
	x, err := n.Int64()
	if err != nil {
		return -1 << 63
	}
	return x
}

func text(v any) string { s, _ := v.(string); return s }

func decode(raw []byte) (object, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var row object
	if err := d.Decode(&row); err != nil || row == nil {
		return nil, invalid("invalid_json")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return nil, invalid("trailing_json")
	}
	return row, nil
}

func readObject(path string) (object, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, invalid("missing_or_unreadable_file")
	}
	defer f.Close()
	s, err := f.Stat()
	if err != nil || !s.Mode().IsRegular() || s.Size() > 1<<20 {
		return nil, invalid("invalid_file_size")
	}
	b, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(b) > 1<<20 {
		return nil, invalid("invalid_file_size")
	}
	return decode(b)
}

// Explicit field lists keep server labels and future/private fields out of
// decision observations. Missing fields are not silently replaced with zeros.
var observationFields = map[string]string{
	"observation":   "my_bid menu_flags mp field_attribute legal_action_mask",
	"visible_actor": "bid graphic level hp max_hp flags ride_flag ride_pet_level ride_pet_hp ride_pet_max_hp",
	"own_actor":     "pet_slot level graphic hp max_hp mp max_mp attack defense speed elements dead active_pet_slot ride_pet_slot",
	"allocation":    "pet_slot raw_units_per_point vitality_raw strength_raw toughness_raw dexterity_raw unspent_points allocated_raw budget_raw level rebirths charm luck base_elements",
	"pet_skill":     "pet_slot slot id field target_type cost",
	"item":          "slot equipped id item_type field target_type quantity magic_id magic_mp modifiers",
}

var labelFields = map[string]string{
	"action_request":    "origin opcode arg1 arg2 parsed mp_before",
	"execution_command": "visibility decision_turn bid command hp mp dead",
	"action_resolution": "visibility decision_turn bid command",
}

func selectFields(row object, fields string) (object, error) {
	result := object{}
	for _, k := range strings.Fields(fields) {
		v, ok := row[k]
		if !ok {
			return nil, invalid("missing_field")
		}
		result[k] = v
	}
	return result, nil
}

func numericArray(v any, length int) bool {
	a, ok := v.([]any)
	if !ok || len(a) != length {
		return false
	}
	for _, n := range a {
		if integer(n) == -1<<63 {
			return false
		}
	}
	return true
}

func validateObservation(kind string, row object) error {
	for k, v := range row {
		switch k {
		case "legal_action_mask":
			// Schema 1 never recorded a complete legal-action mask.
			if v != nil {
				return invalid("unsupported_action_mask")
			}
		case "elements", "base_elements":
			if !numericArray(v, 4) {
				return invalid("invalid_observation_field")
			}
		case "dead", "equipped":
			if _, ok := v.(bool); !ok {
				return invalid("invalid_observation_field")
			}
		case "modifiers":
			mod, ok := v.(map[string]any)
			if !ok {
				return invalid("invalid_item_modifiers")
			}
			selected, err := selectFields(mod, "attack defense speed hp mp")
			if err != nil {
				return err
			}
			for _, value := range selected {
				if integer(value) == -1<<63 {
					return invalid("invalid_item_modifiers")
				}
			}
			row[k] = selected
		default:
			if integer(v) == -1<<63 {
				return invalid("invalid_observation_field")
			}
		}
	}
	if kind == "visible_actor" {
		if bid := integer(row["bid"]); bid < 0 || bid >= 20 {
			return invalid("invalid_visible_actor")
		}
	}
	if kind == "own_actor" || kind == "allocation" || kind == "pet_skill" {
		if slot := integer(row["pet_slot"]); slot < -1 || slot >= 10 || kind == "pet_skill" && slot < 0 {
			return invalid("invalid_pet_slot")
		}
	}
	return nil
}

type observation struct {
	id, turn, side, bid int64
	row                 object
	ended               bool
	requests            []object
}

type match struct {
	metadata, result object
	observations     []*observation
	players          map[int64]object
	commands         map[[2]int64][]object
	resolution       map[int64][]object
}

func load(ctx context.Context, directory string) (*match, error) {
	m := &match{players: map[int64]object{}, commands: map[[2]int64][]object{}, resolution: map[int64][]object{}}
	var err error
	if m.metadata, err = readObject(filepath.Join(directory, "metadata.json")); err != nil {
		return nil, err
	}
	if m.result, err = readObject(filepath.Join(directory, "result.json")); err != nil {
		return nil, err
	}
	id := text(m.metadata["match_id"])
	if id == "" || text(m.metadata["type"]) != "match_start" || integer(m.metadata["seq"]) != 1 || integer(m.metadata["turn"]) != 0 || text(m.result["type"]) != "match_end" {
		return nil, invalid("invalid_match_boundary")
	}
	if m.result["trajectory_complete"] != true || m.result["storage_complete"] != true || integer(m.result["dropped_events"]) != 0 {
		return nil, invalid("incomplete_recording")
	}
	winner, turns, reason := integer(m.result["winner_side"]), integer(m.result["turn"]), text(m.result["end_reason"])
	if winner < -1 || winner > 1 || turns < 0 || turns > 100000 || reason == "" || reason == "defeat" && winner < 0 || reason == "turn_limit" && winner != -1 {
		return nil, invalid("invalid_outcome")
	}
	rules := text(m.metadata["ruleset_id"])
	if b, err := hex.DecodeString(rules); err != nil || len(b) != 32 || text(m.metadata["release"]) == "" {
		return nil, invalid("invalid_rules")
	}
	mode := text(m.metadata["mode"])
	counts, ok := m.metadata["players_per_side"].([]any)
	if (mode != "pvp" && mode != "pve") || !ok || len(counts) != 2 {
		return nil, invalid("invalid_roster")
	}
	players, ok := m.metadata["players"].([]any)
	if !ok || len(players) == 0 || len(players) > 10 {
		return nil, invalid("invalid_roster")
	}
	actual := [2]int64{}
	for _, v := range players {
		p, ok := v.(map[string]any)
		bid, side := integer(p["bid"]), integer(p["side"])
		if !ok || bid < 0 || bid >= 20 || bid%10 >= 5 || side != bid/10 || m.players[bid] != nil {
			return nil, invalid("invalid_roster")
		}
		m.players[bid] = p
		actual[side]++
	}
	if actual[0] != integer(counts[0]) || actual[1] != integer(counts[1]) {
		return nil, invalid("invalid_roster")
	}
	check := func(row object, seq int64) error {
		if integer(row["schema_version"]) != 1 || integer(row["seq"]) != seq || text(row["match_id"]) != id {
			return invalid("sequence_or_identity")
		}
		if n := integer(row["turn"]); n < 0 || n > turns || text(row["type"]) == "" {
			return invalid("invalid_event_turn")
		}
		return nil
	}
	if err := check(m.metadata, 1); err != nil {
		return nil, err
	}
	path := filepath.Join(directory, "events.jsonl")
	f, err := os.Open(path)
	compressed := false
	if errors.Is(err, os.ErrNotExist) {
		f, err = os.Open(path + ".gz")
		compressed = true
	}
	if err != nil {
		return nil, invalid("missing_or_unreadable_file")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxMatchBytes {
		return nil, invalid("invalid_file_size")
	}
	var reader io.Reader = f
	if compressed {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, invalid("invalid_gzip")
		}
		defer gz.Close()
		reader = gz
	}
	limited := &io.LimitedReader{R: reader, N: maxMatchBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	byID := map[int64]*observation{}
	seq := int64(2)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		e, err := decode(scanner.Bytes())
		if err != nil {
			return nil, err
		}
		if err := check(e, seq); err != nil {
			return nil, err
		}
		if err := m.add(e, byID); err != nil {
			return nil, err
		}
		seq++
	}
	if scanner.Err() != nil || limited.N <= 0 {
		return nil, invalid("invalid_event_stream")
	}
	if seq == 2 {
		return nil, invalid("empty_events")
	}
	if err := check(m.result, seq); err != nil {
		return nil, err
	}
	if len(m.observations) == 0 {
		return nil, invalid("missing_observations")
	}
	for _, o := range m.observations {
		if !o.ended {
			return nil, invalid("unfinished_observation")
		}
		if _, err := ownRow(o.row, "own_actors"); err != nil {
			return nil, err
		}
		a, err := ownRow(o.row, "allocations")
		if err != nil {
			return nil, err
		}
		if _, err = budget(a); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func (m *match) add(e object, byID map[int64]*observation) error {
	kind, seq, side, turn := text(e["type"]), integer(e["seq"]), integer(e["side"]), integer(e["turn"])
	if kind == "observation" {
		bid := integer(e["my_bid"])
		if m.players[bid] == nil || side != bid/10 {
			return invalid("invalid_observer")
		}
		row, err := selectFields(e, observationFields[kind])
		if err != nil {
			return err
		}
		if err := validateObservation(kind, row); err != nil {
			return err
		}
		row["observation_id"], row["side"], row["turn"] = e["seq"], e["side"], e["turn"]
		for _, k := range []string{"actors", "own_actors", "allocations", "pet_skills", "items"} {
			row[k] = []object{}
		}
		o := &observation{id: seq, turn: turn, side: side, bid: bid, row: row, requests: []object{}}
		byID[seq] = o
		m.observations = append(m.observations, o)
		return nil
	}
	names := map[string]string{"visible_actor": "actors", "own_actor": "own_actors", "allocation": "allocations", "pet_skill": "pet_skills", "item": "items"}
	if name := names[kind]; name != "" || kind == "observation_end" || kind == "action_request" {
		o := byID[integer(e["observation_id"])]
		if o == nil || o.side != side || o.turn != turn || o.ended != (kind == "action_request") {
			return invalid("broken_observation_reference")
		}
		if kind == "observation_end" {
			o.ended = true
			return nil
		}
		fields := observationFields[kind]
		if kind == "action_request" {
			fields = labelFields[kind]
		}
		row, err := selectFields(e, fields)
		if err != nil {
			return err
		}
		if kind == "action_request" {
			if text(row["origin"]) != "client" && text(row["origin"]) != "server_fallback" || len(text(row["opcode"])) != 1 || integer(row["arg1"]) == -1<<63 || integer(row["arg2"]) == -1<<63 || integer(row["mp_before"]) == -1<<63 {
				return invalid("invalid_action_request")
			}
			if _, ok := row["parsed"].(bool); !ok {
				return invalid("invalid_action_request")
			}
			o.requests = append(o.requests, row)
		} else {
			if err := validateObservation(kind, row); err != nil {
				return err
			}
			o.row[name] = append(o.row[name].([]object), row)
		}
		return nil
	}
	if kind == "execution_command" || kind == "action_resolution" {
		t, bid := integer(e["decision_turn"]), integer(e["bid"])
		if t < 0 || t >= integer(m.result["turn"]) || bid < 0 || bid >= 20 || side != bid/10 || text(e["visibility"]) != "server_only" {
			return invalid("invalid_resolution_label")
		}
		row, err := selectFields(e, labelFields[kind])
		if err != nil {
			return err
		}
		if kind == "execution_command" {
			if !numericArray(row["command"], 3) || integer(row["hp"]) == -1<<63 || integer(row["mp"]) == -1<<63 {
				return invalid("invalid_resolution_label")
			}
			if _, ok := row["dead"].(bool); !ok {
				return invalid("invalid_resolution_label")
			}
			k := [2]int64{t, bid}
			m.commands[k] = append(m.commands[k], row)
		} else {
			if integer(row["command"]) == -1<<63 {
				return invalid("invalid_resolution_label")
			}
			m.resolution[t] = append(m.resolution[t], row)
		}
	}
	return nil
}

func ownRow(o object, field string) (object, error) {
	var result object
	for _, row := range o[field].([]object) {
		if integer(row["pet_slot"]) == -1 {
			if result != nil {
				return nil, invalid("duplicate_own_build")
			}
			result = row
		}
	}
	if result == nil {
		return nil, invalid("missing_own_build")
	}
	return result, nil
}

func budget(a object) (int64, error) {
	if integer(a["raw_units_per_point"]) != 100 {
		return 0, invalid("invalid_point_units")
	}
	var total int64
	for _, k := range []string{"vitality_raw", "strength_raw", "toughness_raw", "dexterity_raw", "unspent_points"} {
		n := integer(a[k])
		if n < 0 || n > 1<<31-1 {
			return 0, invalid("invalid_allocation")
		}
		if k == "unspent_points" {
			n *= 100
		}
		total += n
	}
	if total != integer(a["budget_raw"]) {
		return 0, invalid("point_budget_mismatch")
	}
	if total-integer(a["unspent_points"])*100 != integer(a["allocated_raw"]) {
		return 0, invalid("point_budget_mismatch")
	}
	return total, nil
}

func failureReason(err error) string {
	var cause invalid
	if errors.As(err, &cause) {
		return string(cause)
	}
	return fmt.Sprintf("%T", err)
}
