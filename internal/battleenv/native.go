// Package battleenv connects local learners to the isolated native engine.
// This transport carries client-visible packets, never full engine memory.
package battleenv

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

type Build [4]int

// SkillMask selects a subset of the audited native IDs 1,2,3,60,80,90,110,20.
// Selected skills occupy consecutive slots in that order. It is scenario
// metadata, never a numerical model feature or a license to guess unknown IDs.
type ReservePet struct {
	Build     Build
	SkillMask int
}

// The eighth vocabulary entry is guardian attack; native pets still have at
// most seven usable slots. The mask is scenario metadata, not an input ID.
func ValidPetSkillMask(mask int) bool {
	return mask > 0 && mask <= 255 && bits.OnesCount(uint(mask)) <= 7
}

func SupportedScenario(version string) bool {
	return version == "controlled-battle-v7" || version == "controlled-battle-v8"
}

func CloneReserves(source [][]ReservePet) [][]ReservePet {
	if source == nil {
		return nil
	}
	out := make([][]ReservePet, len(source))
	for i, row := range source {
		out[i] = append([]ReservePet(nil), row...)
	}
	return out
}

type Scenario struct {
	HealingItems  int `json:",omitempty"` // Number of single-use template 1234 items per character, 0..15.
	HealingMagic  int `json:",omitempty"` // 0: bare; 10: single heal; 20: side heal. Same equipment for both teams.
	Seed          int
	Level         int
	MaxTurns      int
	Mode          int
	Builds        []Build        // All side 0 members, then all side 1 members.
	PetBuilds     []Build        // Optional: one pet per member; curated attack/guard/break + four status attacks.
	PetSkillMasks []int          `json:",omitempty"` // Optional side-major active skill sets; omitted means the original seven skills.
	Reserves      [][]ReservePet `json:",omitempty"` // Side-major members; equal 1..2 reserves each, matching active pet budgets.
}

func (s Scenario) Validate() error {
	if s.HealingItems < 0 || s.HealingItems > 15 {
		return fmt.Errorf("healing items must be in 0..15")
	}
	if s.HealingMagic != 0 && s.HealingMagic != 10 && s.HealingMagic != 20 {
		return fmt.Errorf("healing magic must be 0, 10 or 20")
	}
	if s.Seed < 1 || s.Seed > 2147483647 || s.Level < 1 || s.Level > 200 || s.MaxTurns < 1 || s.MaxTurns > 10000 || s.Mode < 1 || s.Mode > 5 || len(s.Builds) != s.Mode*2 {
		return fmt.Errorf("invalid native scenario dimensions")
	}
	if len(s.PetBuilds) != 0 && len(s.PetBuilds) != len(s.Builds) {
		return fmt.Errorf("one pet allocation per member required")
	}
	if len(s.PetSkillMasks) > 0 {
		if len(s.PetBuilds) != len(s.Builds) || len(s.PetSkillMasks) != len(s.Builds) {
			return fmt.Errorf("one active skill mask per pet required")
		}
		for _, mask := range s.PetSkillMasks {
			if !ValidPetSkillMask(mask) {
				return fmt.Errorf("active skills exceed supported vocabulary or seven native slots")
			}
		}
	}
	for _, builds := range [][]Build{s.Builds, s.PetBuilds} {
		budget := 0
		for i, b := range builds {
			sum := 0
			for _, n := range b {
				if n < 1 || n > 10000 {
					return fmt.Errorf("invalid integer allocation")
				}
				sum += n
			}
			if i == 0 {
				budget = sum
			}
			if sum != budget || sum > 10000 {
				return fmt.Errorf("all participants require the same budget, at most 10000 points")
			}
		}
	}
	if len(s.Reserves) > 0 {
		if len(s.Reserves) != len(s.Builds) || len(s.PetBuilds) != len(s.Builds) || len(s.Reserves[0]) < 1 || len(s.Reserves[0]) > 2 {
			return fmt.Errorf("reserve pets require one active pet and 1..2 reserves per member")
		}
		budget := s.PetBuilds[0][0] + s.PetBuilds[0][1] + s.PetBuilds[0][2] + s.PetBuilds[0][3]
		for _, row := range s.Reserves {
			if len(row) != len(s.Reserves[0]) {
				return fmt.Errorf("both teams require equal reserve counts")
			}
			for _, pet := range row {
				total := 0
				for _, n := range pet.Build {
					if n < 1 || n > 10000 {
						return fmt.Errorf("invalid reserve allocation")
					}
					total += n
				}
				if total != budget || !ValidPetSkillMask(pet.SkillMask) {
					return fmt.Errorf("reserve budget or skill vocabulary mismatch")
				}
			}
		}
	}
	return nil
}

func (s Scenario) ValidateEnvironment(version string) error {
	if !SupportedScenario(version) {
		return fmt.Errorf("unsupported controlled scenario %q", version)
	}
	if version == "controlled-battle-v7" {
		if len(s.PetSkillMasks) != 0 {
			return fmt.Errorf("active skill configuration requires controlled-battle-v8")
		}
		for _, row := range s.Reserves {
			for _, pet := range row {
				if pet.SkillMask > 127 {
					return fmt.Errorf("guardian skill requires controlled-battle-v8")
				}
			}
		}
	}
	return s.Validate()
}

type Packet struct {
	Function string `json:"function"`
	Hex      string `json:"hex"`
}
type Member struct {
	Side    int      `json:"side"`
	Seat    int      `json:"seat"`
	Packets []Packet `json:"packets"`
}
type frame struct {
	Schema      int      `json:"schema_version"`
	OK          bool     `json:"ok"`
	Ready       bool     `json:"ready"`
	Scenario    string   `json:"scenario"`
	RulesDigest string   `json:"rules_digest"`
	Platform    string   `json:"platform"`
	Error       string   `json:"error"`
	Mode        int      `json:"mode"`
	Turn        int      `json:"turn"`
	Terminated  bool     `json:"terminated"`
	Truncated   bool     `json:"truncated"`
	Winner      int      `json:"winner_side"`
	Members     []Member `json:"members"`
}

// Step is an immutable observation returned after both sides have resolved.
// Views and Packets use side-major ordering; a single side's policy must only
// receive that side's slice. Packets are retained for reproducible projection.
type Step struct {
	Match                 string
	Mode, Turn            int
	Terminated, Truncated bool
	Winner                int
	Views                 []aigame.BattleView
	Events                []aigame.BattleEventBatch // Incremental batches, one per member, same cutoff as Views.
	Packets               []Member
}

// Metadata distinguishes the actual executable/table digest from the wire
// schema and controlled scenario version. Older low-level workers may omit it;
// training and checkpoint resume require complete, equal metadata.
type Metadata struct {
	Rules    string `json:"rules_digest"`
	Platform string `json:"platform"`
	Scenario string `json:"scenario"`
}

func (m Metadata) Validate() error {
	r, e := hex.DecodeString(m.Rules)
	if e != nil || len(r) != 32 || len(m.Rules) != 64 || !SupportedScenario(m.Scenario) || (m.Platform != "linux-amd64" && m.Platform != "linux-arm64") {
		return fmt.Errorf("verified rules digest, supported platform and controlled scenario required")
	}
	return nil
}
func (n *Native) Metadata() Metadata { return n.metadata }

// Native owns one sequential engine process. Cancelling any request poisons
// and terminates that worker: an uncertain step must never be retried.
type Native struct {
	mu         sync.Mutex
	cmd        *exec.Cmd
	input      io.WriteCloser
	output     io.ReadCloser
	frames     chan frame
	done       chan struct{}
	stopping   chan struct{}
	stop       sync.Once
	closeOnce  sync.Once
	closeErr   error
	docker     *dockerOwner
	err        error // Read only after done closes.
	generation uint64
	namespace  string
	scenario   Scenario
	current    Step
	replay     []*aigame.BattleReplay
	streams    []string
	cursors    []uint64
	dead       bool
	metadata   Metadata
}

func Start(ctx context.Context, command []string, stderr io.Writer) (*Native, error) {
	if len(command) == 0 {
		return nil, fmt.Errorf("native engine command required")
	}
	var identity [16]byte
	if _, e := rand.Read(identity[:]); e != nil {
		return nil, e
	}
	command, owner, e := ownDockerCommand(command, hex.EncodeToString(identity[:]))
	if e != nil {
		return nil, e
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	if owner != nil {
		owner.executable, owner.environment = cmd.Path, cmd.Environ()
		cmd.Env = owner.environment
	}
	cmd.Stderr = stderr
	input, e := cmd.StdinPipe()
	if e != nil {
		return nil, e
	}
	output, e := cmd.StdoutPipe()
	if e != nil {
		input.Close()
		return nil, e
	}
	n := &Native{cmd: cmd, input: input, output: output, frames: make(chan frame, 1), done: make(chan struct{}), stopping: make(chan struct{}), namespace: hex.EncodeToString(identity[:]), docker: owner}
	if e = cmd.Start(); e != nil {
		input.Close()
		output.Close()
		return nil, e
	}
	go func() {
		defer close(n.done)
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 65536), 8<<20)
		for scanner.Scan() {
			var f frame
			if e := json.Unmarshal(scanner.Bytes(), &f); e != nil {
				n.err = fmt.Errorf("invalid engine frame: %w", e)
				n.abort()
				break
			}
			select {
			case n.frames <- f:
			case <-n.stopping:
			}
		}
		if e := scanner.Err(); e != nil && n.err == nil {
			n.err = e
		}
		if e := cmd.Wait(); e != nil && n.err == nil {
			n.err = e
		}
	}()
	f, e := n.receive(ctx)
	if e == nil && (f.Schema != 1 || !f.OK || !f.Ready || !SupportedScenario(f.Scenario)) {
		e = fmt.Errorf("incompatible native engine greeting")
	}
	if e != nil {
		n.abort()
		return nil, errors.Join(e, n.Close())
	}
	n.metadata = Metadata{Rules: f.RulesDigest, Platform: f.Platform, Scenario: f.Scenario}
	return n, nil
}

func (n *Native) receive(ctx context.Context) (frame, error) {
	select {
	case f := <-n.frames:
		if f.Schema != 1 || !f.OK {
			return f, fmt.Errorf("native engine rejected request: %s", f.Error)
		}
		return f, nil
	case <-ctx.Done():
		return frame{}, ctx.Err()
	case <-n.done:
		select {
		case f := <-n.frames:
			if f.Schema == 1 && f.OK {
				return f, nil
			}
		default:
		}
		return frame{}, fmt.Errorf("native engine exited: %w", errors.Join(io.EOF, n.err))
	}
}
func (n *Native) abort() {
	n.stopInput()
	_ = n.cmd.Process.Kill()
	// A wrapper's child may still hold stdout after the wrapper is killed.
	// Stop our reader too, so cleanup never waits for an inherited pipe.
	_ = n.output.Close()
}
func (n *Native) stopInput() {
	n.stop.Do(func() { close(n.stopping); _ = n.input.Close() })
}

// Close sends EOF and waits for the native worker to drain its archive writer.
// Cancellation/failed requests still abort immediately. A stuck shutdown is
// bounded and reported as a failure, never mistaken for a successful flush.
func (n *Native) Close() error { return n.closeWithin(10 * time.Second) }

func (n *Native) closeWithin(timeout time.Duration) error {
	n.closeOnce.Do(func() {
		n.stopInput()
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case <-n.done:
			n.closeErr = n.err
		case <-timer.C:
			n.abort()
			<-n.done
			n.closeErr = errors.Join(fmt.Errorf("native worker shutdown: %w", context.DeadlineExceeded), n.err)
		}
		n.closeErr = errors.Join(n.closeErr, n.docker.cleanup())
	})
	return n.closeErr
}

func (n *Native) exchange(ctx context.Context, line string) (frame, error) {
	if n.dead {
		return frame{}, fmt.Errorf("native worker is unusable after failed request")
	}
	select {
	case <-n.stopping:
		return frame{}, fmt.Errorf("native worker is closed")
	default:
	}
	if e := ctx.Err(); e != nil {
		return frame{}, e
	}
	// Requests are bounded below pipe capacity. Cancellation also interrupts a
	// worker stuck before reading, rather than leaving a blocked writer behind.
	written := make(chan error, 1)
	go func() { _, e := io.WriteString(n.input, line+"\n"); written <- e }()
	var e error
	select {
	case e = <-written:
	case <-ctx.Done():
		e = ctx.Err()
		n.abort()
		<-written
	}
	var f frame
	if e == nil {
		f, e = n.receive(ctx)
	}
	if e != nil {
		n.dead = true
		n.abort()
	}
	return f, e
}

func (n *Native) Reset(ctx context.Context, s Scenario) (Step, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if e := s.ValidateEnvironment(n.metadata.Scenario); e != nil {
		return Step{}, e
	}
	fields := []string{"reset", strconv.Itoa(s.Seed), strconv.Itoa(s.Level), strconv.Itoa(s.MaxTurns), strconv.Itoa(s.Mode)}
	if len(s.PetBuilds) > 0 {
		fields[0] = "reset-pets"
	}
	for _, b := range append(append([]Build(nil), s.Builds...), s.PetBuilds...) {
		for _, v := range b {
			fields = append(fields, strconv.Itoa(v))
		}
	}
	if len(s.Reserves) > 0 || len(s.PetSkillMasks) > 0 {
		fields[0] = "reset-pets-roster"
		count := 0
		if len(s.Reserves) > 0 {
			count = len(s.Reserves[0])
		}
		fields = append(fields, strconv.Itoa(s.HealingMagic), strconv.Itoa(s.HealingItems), strconv.Itoa(count))
		if len(s.PetSkillMasks) > 0 {
			fields[0] = "reset-pets-skills-roster"
			for _, mask := range s.PetSkillMasks {
				fields = append(fields, strconv.Itoa(mask))
			}
		}
		for _, row := range s.Reserves {
			for _, pet := range row {
				for _, v := range pet.Build {
					fields = append(fields, strconv.Itoa(v))
				}
				fields = append(fields, strconv.Itoa(pet.SkillMask))
			}
		}
	} else if s.HealingItems != 0 {
		fields[0] += "-loadout"
		fields = append(fields, strconv.Itoa(s.HealingMagic), strconv.Itoa(s.HealingItems))
	} else if s.HealingMagic != 0 {
		fields[0] += "-healing"
		fields = append(fields, strconv.Itoa(s.HealingMagic))
	}
	f, e := n.exchange(ctx, strings.Join(fields, " "))
	if e != nil {
		return Step{}, e
	}
	n.generation++
	n.scenario = s
	match := fmt.Sprintf("offline-%s-%d", n.namespace, n.generation)
	n.replay = make([]*aigame.BattleReplay, s.Mode*2)
	n.streams = make([]string, s.Mode*2)
	n.cursors = make([]uint64, s.Mode*2)
	for i := range n.replay {
		n.replay[i], _ = aigame.NewBattleReplay(match, s.Mode)
	}
	n.current = Step{Match: match}
	if f.Turn != 0 {
		return n.badFrame("reset must start at decision turn zero")
	}
	return n.project(f)
}

// Advance accepts complete side-major choices. It resolves every candidate
// before writing any bytes, preventing partial joint decisions on bad input.
func (n *Native) Advance(ctx context.Context, choices [][]aigame.BattleSelection) (Step, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.replay) == 0 || n.current.Terminated || n.current.Truncated {
		return Step{}, fmt.Errorf("reset required before step")
	}
	if len(choices) != len(n.replay) {
		return Step{}, fmt.Errorf("one choice per member required")
	}
	fields := []string{"step", strconv.Itoa(n.current.Turn)}
	for i, memberChoices := range choices {
		commands := map[string]string{}
		view := n.replay[i].View(int32(n.current.Turn))
		if view.Withdrawn {
			if len(memberChoices) != 0 || len(view.Candidates) != 0 {
				return Step{}, fmt.Errorf("withdrawn member cannot receive orders")
			}
			// Fixed-width transport placeholders; the shared native arena
			// guard rejects commands for an entry already removed by knockout.
			fields = append(fields, "N")
			if len(n.scenario.PetBuilds) > 0 {
				fields = append(fields, "-")
			}
			continue
		}
		expected := map[string]bool{}
		candidates := map[string]aigame.BattleCandidate{}
		for _, c := range view.Candidates {
			candidates[c.ID] = c
			expected[c.Actor] = true
		}
		resolved, e := n.replay[i].ResolvePlan(int32(n.current.Turn), memberChoices)
		if e != nil {
			return Step{}, e
		}
		for ci, choice := range memberChoices {
			candidate, ok := candidates[choice.CandidateID]
			if !ok {
				return Step{}, fmt.Errorf("unknown candidate")
			}
			if commands[candidate.Actor] != "" {
				return Step{}, fmt.Errorf("duplicate actor choice")
			}
			a := resolved[ci]
			switch candidate.Actor {
			case "player":
				heal := n.scenario.HealingMagic != 0 && candidate.Kind == "magic" && candidate.Index == 1 && candidate.MagicIDKnown && int(candidate.MagicID) == n.scenario.HealingMagic
				item := n.scenario.HealingItems > 0 && candidate.Kind == "item" && candidate.ItemTemplateIDKnown && candidate.ItemTemplateID == 1234
				switchPet := len(n.scenario.PetBuilds) > 0 && candidate.Kind == "switch_pet" && strings.HasPrefix(a.Command, "S|")
				if a.Command != "G" && a.Command != "N" && !strings.HasPrefix(a.Command, "H|") && !heal && !item && !switchPet {
					return Step{}, fmt.Errorf("unsupported player action in controlled scenario")
				}
			case "pet":
				if len(n.scenario.PetBuilds) == 0 || !strings.HasPrefix(a.Command, "W|") {
					return Step{}, fmt.Errorf("unsupported pet action")
				}
			default:
				return Step{}, fmt.Errorf("unknown actor")
			}
			commands[candidate.Actor] = a.Command
		}
		if len(commands) != len(expected) || commands["player"] == "" {
			return Step{}, fmt.Errorf("complete plan required for each member")
		}
		fields = append(fields, commands["player"])
		if len(n.scenario.PetBuilds) > 0 {
			pet := commands["pet"]
			if pet == "" {
				pet = "-"
			}
			fields = append(fields, pet)
		}
	}

	f, e := n.exchange(ctx, strings.Join(fields, " "))
	if e != nil {
		return Step{}, e
	}
	if f.Turn != n.current.Turn+1 {
		return n.badFrame("step did not advance exactly one decision turn")
	}
	return n.project(f)
}
func (n *Native) badFrame(reason string) (Step, error) {
	n.dead = true
	n.abort()
	return Step{}, fmt.Errorf("invalid engine observation: %s", reason)
}
func (n *Native) project(f frame) (Step, error) {
	if f.Mode != n.scenario.Mode || len(f.Members) != len(n.replay) || f.Turn < 0 || f.Turn > n.scenario.MaxTurns || f.Terminated && f.Truncated || f.Winner < -1 || f.Winner > 1 || !f.Terminated && f.Winner != -1 || f.Truncated != (f.Turn >= n.scenario.MaxTurns && !f.Terminated) {
		return n.badFrame("mode, terminal or member mismatch")
	}
	// Validate all packet envelopes before mutating the projection.
	for i, m := range f.Members {
		if m.Side != i/f.Mode || m.Seat != i%f.Mode || len(m.Packets) > 260 {
			return n.badFrame("invalid member identity or packet count")
		}
		for _, p := range m.Packets {
			if p.Function != "B" && p.Function != "S" || len(p.Hex) == 0 || len(p.Hex) > 524288 {
				return n.badFrame("invalid packet")
			}
			if _, e := hex.DecodeString(p.Hex); e != nil {
				return n.badFrame("invalid packet hex")
			}
		}
	}
	result := Step{Match: n.current.Match, Mode: f.Mode, Turn: f.Turn, Terminated: f.Terminated, Truncated: f.Truncated, Winner: f.Winner, Packets: f.Members}
	for i, m := range f.Members {
		for _, p := range m.Packets {
			b, _ := hex.DecodeString(p.Hex)
			if e := n.replay[i].Apply(p.Function, b); e != nil {
				return n.badFrame(e.Error())
			}
		}
		v := n.replay[i].View(int32(f.Turn))
		if !v.Battle.MyNoKnown || int(v.Battle.MyNo) != m.Side*10+m.Seat || !v.Own.HasStatus {
			return n.badFrame("missing native own-state/menu")
		}
		result.Views = append(result.Views, v)
		batch := n.replay[i].Events(int32(f.Turn), n.streams[i], n.cursors[i])
		n.streams[i], n.cursors[i] = batch.Stream, batch.Cursor
		result.Events = append(result.Events, batch)
	}
	n.current = result
	return result, nil
}
