package arenaagent

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
	"github.com/k0ngk0ng/stoneage/internal/battletrain"
)

type DemonstrationImportReport struct {
	Event    string                             `json:"event"`
	Output   string                             `json:"output"`
	Matches  int                                `json:"matches_seen"`
	Imported int                                `json:"imported_matches"`
	Excluded map[string]int                     `json:"excluded"`
	Dataset  *battletrain.DemonstrationManifest `json:"dataset,omitempty"`
}

func recordedDatabaseHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<30 {
		return "", fmt.Errorf("recorded database must be a regular file of at most 1 GiB")
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, (1<<30)+1))
	if err != nil {
		return "", err
	}
	if n != info.Size() {
		return "", fmt.Errorf("recorded database changed size")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func requireCheckpointedDatabase(path string) error {
	for _, suffix := range []string{"-wal", "-journal"} {
		if info, err := os.Stat(path + suffix); err == nil {
			if info.Size() > 0 {
				return fmt.Errorf("stop the collector and checkpoint its database before import; nonempty %s", suffix)
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// These databases must be stopped snapshots, particularly when a Linux
// collector mounted them from Docker. Immutable read-only access avoids
// opening Linux shared-memory/WAL files through a Mac SQLite process. Checks
// cannot detect every active rollback-journal writer; stopping is a caller
// precondition, not a claim inferred from absent WAL files.
func recordedDemonstrations(ctx context.Context, path, features string, report *DemonstrationImportReport) ([]battletrain.Demonstration, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	if err := requireCheckpointedDatabase(path); err != nil {
		return nil, err
	}
	before, err := recordedDatabaseHash(path)
	if err != nil {
		return nil, err
	}
	uri := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&immutable=1"}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var integrity string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&integrity); err != nil {
		return nil, err
	}
	if integrity != "ok" {
		return nil, fmt.Errorf("recorded database integrity check failed")
	}
	rows, err := db.QueryContext(ctx, "SELECT match_id,body FROM results ORDER BY match_id LIMIT 10001")
	if err != nil {
		return nil, err
	}
	results := map[string]Object{}
	for rows.Next() {
		var id, body string
		if err = rows.Scan(&id, &body); err != nil {
			break
		}
		if len(body) > 1<<20 {
			err = fmt.Errorf("result exceeds size limit")
			break
		}
		var result Object
		if err = decode([]byte(body), &result); err != nil {
			break
		}
		if id == "" || id != str(result["id"]) {
			err = fmt.Errorf("result identity differs from database index")
			break
		}
		results[id] = result
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if rowErr != nil {
		return nil, rowErr
	}
	if len(results) > 10000 {
		return nil, fmt.Errorf("too many recorded matches; export a smaller stopped database")
	}
	// Count unsettled matches as exclusions rather than hiding them merely
	// because the results table has no row for them.
	var unsettled int
	if err := db.QueryRowContext(ctx, "SELECT count(DISTINCT match_id) FROM records WHERE kind='turn' AND match_id NOT IN (SELECT match_id FROM results)").Scan(&unsettled); err != nil {
		return nil, err
	}
	report.Matches += unsettled
	if unsettled > 0 {
		report.Excluded["unsettled_match"] += unsettled
	}
	var episodes []battletrain.Demonstration
	var encodedBytes int64
	for _, id := range sortedKeys(results) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		report.Matches++
		d, reason, err := recordedMatch(ctx, db, before, features, id, results[id])
		if err != nil {
			return nil, err
		}
		if reason != "" {
			report.Excluded[reason]++
			continue
		}
		encodedBytes += int64(len(enc(d))) + 1
		if encodedBytes > 256<<20 {
			return nil, fmt.Errorf("demonstration dataset exceeds 256 MiB; use a smaller stopped snapshot")
		}
		episodes = append(episodes, d)
		report.Imported++
	}
	if err := db.Close(); err != nil {
		return nil, err
	}
	if err := requireCheckpointedDatabase(path); err != nil {
		return nil, err
	}
	after, err := recordedDatabaseHash(path)
	if err != nil {
		return nil, err
	}
	if before != after {
		return nil, fmt.Errorf("recorded database changed during import")
	}
	return episodes, nil
}

type recordedTurn struct {
	id   int64
	body Object
}

func recordedMatch(ctx context.Context, db *sql.DB, source, features, match string, result Object) (battletrain.Demonstration, string, error) {
	d := battletrain.Demonstration{Schema: battletrain.DemonstrationSchema, Source: source, Match: match, Features: features, GroupKind: "recorded-roster-v1", Mode: integer(result["mode"]), Winner: integer(result["winner_side"]), Reason: str(result["reason"]), Turns: integer(result["turns"]), ResultDigest: hash(result)}
	var envelope struct {
		Mode   *int `json:"mode"`
		Turns  *int `json:"turns"`
		Winner *int `json:"winner_side"`
	}
	if decode(enc(result), &envelope) != nil || envelope.Mode == nil || envelope.Turns == nil || envelope.Winner == nil {
		return d, "noncombat_or_invalid_result", nil
	}
	if d.Mode < 1 || d.Mode > 5 || d.Turns < 1 || d.Turns > 10000 || d.Reason != "defeat" || result["winner_side"] == nil || d.Winner < 0 || d.Winner > 1 {
		return d, "noncombat_or_invalid_result", nil
	}
	roster := []string{}
	seen := map[string]bool{}
	for _, p := range objects(result["members"]) {
		id := str(p["id"])
		if id == "" || seen[id] {
			return d, "incomplete_result_roster", nil
		}
		seen[id] = true
		roster = append(roster, id)
	}
	if len(roster) != d.Mode*2 {
		return d, "incomplete_result_roster", nil
	}
	sort.Strings(roster)
	d.Roster = roster
	d.Group = battletrain.DemonstrationGroup(d.Mode, roster)
	var gaps int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM records WHERE match_id=? AND kind IN ('event_gap','strategy_fallback')", match).Scan(&gaps); err != nil {
		return d, "", err
	}
	if gaps != 0 {
		return d, "gap_or_fallback", nil
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM battle_intents WHERE match_id=? AND status!='written'", match).Scan(&gaps); err != nil {
		return d, "", err
	}
	if gaps != 0 {
		return d, "uncertain_submission", nil
	}
	var pinned string
	if err := db.QueryRowContext(ctx, "SELECT policy FROM match_policies WHERE match_id=?", match).Scan(&pinned); err == sql.ErrNoRows {
		return d, "missing_pinned_policy", nil
	} else if err != nil {
		return d, "", err
	}
	rows, err := db.QueryContext(ctx, "SELECT id,body FROM records WHERE kind='turn' AND match_id=? ORDER BY id LIMIT 20001", match)
	if err != nil {
		return d, "", err
	}
	var turns []recordedTurn
	for rows.Next() {
		var record recordedTurn
		var body string
		if err = rows.Scan(&record.id, &body); err != nil {
			break
		}
		if len(body) > 16<<20 {
			err = fmt.Errorf("recorded turn exceeds size limit")
			break
		}
		if err = decode([]byte(body), &record.body); err != nil {
			break
		}
		turns = append(turns, record)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return d, "", err
	}
	if rowErr != nil {
		return d, "", rowErr
	}
	if len(turns) > 20000 {
		return d, "too_many_decisions", nil
	}
	var historyBytes, historyEvents int64
	if err := db.QueryRowContext(ctx, "SELECT coalesce(sum(length(body)),0),count(*) FROM events WHERE match_id=?", match).Scan(&historyBytes, &historyEvents); err != nil {
		return d, "", err
	}
	if historyBytes > 64<<20 || historyEvents > 100000 {
		return d, "history_size_limit", nil
	}
	store := &Store{DB: db}
	history := store.History(match)
	if err := store.Err(); err != nil {
		return d, "", err
	}
	byTurn := map[int][]recordedTurn{}
	for _, row := range turns {
		team := obj(row.body["team"])
		turn := integer(team["turn"])
		if str(team["match_id"]) != match || team["turn"] == nil || turn < 0 || turn >= d.Turns {
			return d, "mixed_or_out_of_range_turn", nil
		}
		byTurn[turn] = append(byTurn[turn], row)
	}
	if len(byTurn) != d.Turns {
		return d, "missing_decision_turn", nil
	}
	var stream string
	var cursor uint64
	for turn := 0; turn < d.Turns; turn++ {
		if err := ctx.Err(); err != nil {
			return d, "", err
		}
		var accepted *battletrain.DemonstratedTurn
		for _, record := range byTurn[turn] {
			row, team := record.body, obj(record.body["team"])
			strategy, policy := str(row["strategy"]), str(row["version"])
			if strategy+":"+policy != pinned {
				return d, "policy_changed_or_fallback", nil
			}
			if turn == 0 {
				d.Strategy, d.Policy = strategy, policy
			}
			plan, err := parsePlan(enc(row["plan"]), team)
			if err != nil {
				continue
			}
			l := &Learned{mode: d.Mode}
			t, err := l.neuralTeam(team)
			if err != nil {
				continue
			}
			for _, view := range t.views {
				clock := view.Battle.Clock
				if digest, err := hex.DecodeString(clock.RulesDigest); err != nil || len(digest) != 32 || clock.EnginePlatform == "" || clock.RulesVersion != RulesVersion {
					return d, "missing_rules_metadata", nil
				}
			}
			h, err := neuralHistory(t, history, stream, cursor, turn == 0)
			if err != nil {
				continue
			}
			frame, err := battlepolicy.EncodeVersion(t.views, h, features)
			if err != nil || frame.Events[12] != 0 {
				continue
			}
			for _, v := range t.views {
				if !seen[v.CharacterID] {
					return d, "observation_not_in_result_roster", nil
				}
			}
			choices := make(map[Slot]string, len(plan.Orders))
			for _, order := range plan.Orders {
				choices[Slot{order.Member, order.Actor}] = order.Candidate
			}
			if len(choices) != len(frame.Slots) {
				continue
			}
			s := battletrain.DemonstratedTurn{RecordID: record.id, Frame: frame, Observations: t.views, History: *h.Batch, InitialCursor: h.InitialCursor}
			valid := true
			for _, slot := range frame.Slots {
				member := t.names[slot.Member]
				id := choices[Slot{member, slot.Actor}]
				index := -1
				for i, c := range slot.Candidates {
					if c.ID == id && c.Supported {
						index = i
						break
					}
				}
				if index < 0 {
					valid = false
					break
				}
				var status, body string
				err := db.QueryRowContext(ctx, "SELECT status,body FROM battle_intents WHERE member=? AND match_id=? AND turn=? AND actor=?", member, match, turn, slot.Actor).Scan(&status, &body)
				if err != nil && err != sql.ErrNoRows {
					return d, "", err
				}
				var selection aigame.BattleSelection
				want := aigame.BattleSelection{MatchID: match, Turn: int32(turn), ObservationID: slot.Observation, CandidateID: id}
				if err != nil || status != "written" || decode([]byte(body), &selection) != nil || selection != want {
					valid = false
					break
				}
				s.Choices = append(s.Choices, index)
				s.Submissions = append(s.Submissions, battletrain.DemonstratedSubmission{Member: slot.Member, Actor: slot.Actor, Status: status, Selection: selection})
			}
			if !valid {
				continue
			}
			accepted = &s
			break // Subsequent partial-plan reuse cannot add a second sample.
		}
		if accepted == nil {
			return d, "unreconstructable_or_unwritten_plan", nil
		}
		if turn == 0 {
			d.Side = accepted.Frame.Side
			clock := accepted.Observations[0].Battle.Clock
			d.Rules, d.Platform = clock.RulesDigest, clock.EnginePlatform
		}
		d.Steps = append(d.Steps, *accepted)
		stream, cursor = accepted.History.Stream, accepted.History.Cursor
	}
	var written int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM battle_intents WHERE match_id=? AND status='written'", match).Scan(&written); err != nil {
		return d, "", err
	}
	planned := 0
	for _, s := range d.Steps {
		planned += len(s.Submissions)
	}
	if planned != written {
		return d, "unrepresented_written_intent", nil
	}
	if err := d.Validate(); err != nil {
		return d, "invalid_demonstration_provenance", nil
	}
	return d, "", nil
}

func ImportDemonstrations(ctx context.Context, paths []string, output, features string) (DemonstrationImportReport, error) {
	r := DemonstrationImportReport{Event: "demonstrations_imported", Output: output, Excluded: map[string]int{}}
	if len(paths) == 0 || len(paths) > 100 || output == "" || !battlepolicy.SupportedFeatures(features) {
		return r, fmt.Errorf("import requires 1..100 stopped databases, new output and supported feature contract")
	}
	var episodes []battletrain.Demonstration
	var totalBytes int64
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return r, err
		}
		rows, err := recordedDemonstrations(ctx, path, features, &r)
		if err != nil {
			return r, err
		}
		for _, d := range rows {
			totalBytes += int64(len(enc(d))) + 1
		}
		if totalBytes > 256<<20 || len(episodes)+len(rows) > 10000 {
			return r, fmt.Errorf("combined demonstration dataset exceeds size/count limit")
		}
		episodes = append(episodes, rows...)
	}
	if len(episodes) == 0 {
		return r, fmt.Errorf("no complete eligible demonstrations; inspect exclusion counts")
	}
	m, err := battletrain.SaveDemonstrations(ctx, output, episodes)
	if err != nil {
		return r, err
	}
	r.Dataset = &m
	return r, nil
}
