// Package arenaagent implements a local squad commander over the public sactl
// interface. Strategies produce plans; only the runner may submit commands.
package arenaagent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

type Object = map[string]any

func obj(v any) Object { m, _ := v.(map[string]any); return m }
func arr(v any) []any  { a, _ := v.([]any); return a }
func str(v any) string { s, _ := v.(string); return s }
func num(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return 0
}
func integer(v any) int { return int(num(v)) }
func yes(v any) bool    { b, _ := v.(bool); return b }
func enc(v any) []byte  { b, _ := json.Marshal(v); return b }
func hash(v any) string { h := sha256.Sum256(enc(v)); return hex.EncodeToString(h[:]) }
func decode(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return fmt.Errorf("expected one JSON value")
	}
	return nil
}
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
func contains(a []any, s string) bool {
	for _, v := range a {
		if str(v) == s {
			return true
		}
	}
	return false
}
func objects(a any) []Object {
	out := []Object{}
	for _, v := range arr(a) {
		out = append(out, obj(v))
	}
	return out
}
func clone(v Object) Object { var m Object; _ = decode(enc(v), &m); return m }

type Slot struct{ Member, Actor string }
type Order struct {
	Member    string `json:"member_id"`
	Actor     string `json:"actor"`
	Candidate string `json:"candidate_id"`
}
type Plan struct {
	Schema      int     `json:"schema_version"`
	Match       string  `json:"match_id"`
	Turn        int     `json:"turn"`
	Observation string  `json:"observation_id"`
	Orders      []Order `json:"orders"`
}
type Decision struct {
	Plan              Plan
	Strategy, Version string
	Diagnostics       Object
}

func teamObservation(views Object, expected map[string]bool, mode int) (Object, error) {
	if len(views) == 0 || mode < 1 || mode > 5 || len(expected) != mode {
		return nil, fmt.Errorf("invalid or empty squad")
	}
	match, rules := "", ""
	turn, side := -1, -1
	ids := map[string]bool{}
	observations := Object{}
	for _, member := range sortedKeys(views) {
		v := obj(views[member])
		b := obj(v["battle"])
		id := str(v["character_id"])
		seat := integer(b["MyNo"])
		if integer(v["schema_version"]) != 1 || !expected[id] || ids[id] || !yes(b["Active"]) || yes(b["Ended"]) || !yes(b["MyNoKnown"]) || seat < 0 || seat >= 15 || seat%10 >= 5 || integer(v["mode"]) != mode || str(v["match_id"]) == "" {
			return nil, fmt.Errorf("invalid squad observation for %s", member)
		}
		r := str(obj(b["Clock"])["RulesVersion"])
		if turn >= 0 && (match != str(v["match_id"]) || turn != integer(v["turn"]) || side != seat/10 || rules != r) {
			return nil, fmt.Errorf("mixed match, turn, side or rules")
		}
		ids[id] = true
		match = str(v["match_id"])
		turn = integer(v["turn"])
		side = seat / 10
		rules = r
		observations[member] = v["observation_id"]
	}
	missing := []string{}
	for id := range expected {
		if !ids[id] {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	return Object{"schema_version": 1, "match_id": match, "turn": turn, "mode": mode, "side": side, "members": views, "missing_ids": missing, "rules_version": rules, "observation_id": hash(observations)}, nil
}
func slots(team Object) map[Slot]map[string]Object {
	out := map[Slot]map[string]Object{}
	for member, value := range obj(team["members"]) {
		v := obj(value)
		b := obj(v["battle"])
		for _, c := range objects(v["candidates"]) {
			actor := str(c["actor"])
			if actor != "player" && actor != "pet" {
				continue
			}
			field := "PlayerSubmitted"
			if actor == "pet" {
				field = "PetSubmitted"
			}
			if yes(b[field]) || obj(v["reserved_actors"])[actor] != nil {
				continue
			}
			key := Slot{member, actor}
			if out[key] == nil {
				out[key] = map[string]Object{}
			}
			out[key][str(c["id"])] = c
		}
	}
	return out
}
func sortedSlots(m map[Slot]map[string]Object) []Slot {
	a := make([]Slot, 0, len(m))
	for k := range m {
		a = append(a, k)
	}
	sort.Slice(a, func(i, j int) bool {
		if a[i].Member != a[j].Member {
			return a[i].Member < a[j].Member
		}
		return a[i].Actor < a[j].Actor
	})
	return a
}
func planFor(t Object, choices map[Slot]string) Plan {
	p := Plan{Schema: 1, Match: str(t["match_id"]), Turn: integer(t["turn"]), Observation: str(t["observation_id"]), Orders: []Order{}}
	for k, id := range choices {
		p.Orders = append(p.Orders, Order{k.Member, k.Actor, id})
	}
	sort.Slice(p.Orders, func(i, j int) bool {
		a, b := p.Orders[i], p.Orders[j]
		if a.Member != b.Member {
			return a.Member < b.Member
		}
		return a.Actor < b.Actor
	})
	return p
}
func validatePlan(t Object, p Plan) error {
	if p.Schema != 1 || p.Match != str(t["match_id"]) || p.Turn != integer(t["turn"]) || p.Observation != str(t["observation_id"]) {
		return fmt.Errorf("stale or invalid plan identity")
	}
	available := slots(t)
	if len(p.Orders) != len(available) {
		return fmt.Errorf("one order per unsubmitted actor is required")
	}
	seen := map[Slot]bool{}
	for _, o := range p.Orders {
		k := Slot{o.Member, o.Actor}
		if seen[k] || available[k][o.Candidate] == nil {
			return fmt.Errorf("duplicate, foreign or unavailable order")
		}
		seen[k] = true
	}
	return nil
}
func parsePlan(raw []byte, t Object) (Plan, error) {
	var p Plan
	if uniqueJSON(raw) != nil {
		return p, fmt.Errorf("invalid or duplicate plan JSON field")
	}
	var m Object
	if err := decode(raw, &m); err != nil {
		return p, fmt.Errorf("invalid plan JSON")
	}
	if len(m) != 5 {
		return p, fmt.Errorf("plan must match exact schema")
	}
	for _, key := range []string{"schema_version", "turn"} {
		if _, ok := m[key].(float64); !ok {
			return p, fmt.Errorf("plan requires numeric %s", key)
		}
	}
	for _, key := range []string{"match_id", "observation_id"} {
		if _, ok := m[key].(string); !ok {
			return p, fmt.Errorf("plan requires string %s", key)
		}
	}
	if _, ok := m["orders"].([]any); !ok {
		return p, fmt.Errorf("orders must be an array")
	}
	for _, o := range objects(m["orders"]) {
		if len(o) != 3 {
			return p, fmt.Errorf("invalid order schema")
		}
		for _, key := range []string{"member_id", "actor", "candidate_id"} {
			if _, ok := o[key].(string); !ok {
				return p, fmt.Errorf("order fields must be strings")
			}
		}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return p, fmt.Errorf("invalid plan schema")
	}
	return p, validatePlan(t, p)
}
