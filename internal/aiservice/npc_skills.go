package aiservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

// The NPC catalog is deliberately separate from aiknowledge.  A knowledge
// snapshot can describe an NPC source file, but it cannot prove that the
// object is present on the currently connected server.  NPCRegistry is
// installed by the authenticated server/session owner and must be treated as
// immutable after startup.
type NPCRegistry map[string]NPCSpec

// NPCWindowSpec is one server-verified window in an NPC conversation. A
// single NPC may expose several windows over time (for example sequence 100
// selects a course and sequence 110 confirms its price), so identity and
// choices belong to the sequence rather than to NPCSpec as a whole.
//
// Type/Sequence/ObjectID are the preferred field names. The Window* aliases
// are accepted for callers that build contracts beside aimcp.WindowState;
// when both spellings are present they must agree.
type NPCWindowSpec struct {
	Type     int
	Sequence int
	ObjectID int
	Choices  map[int]NPCChoice
	// WindowObjectFromActor makes ObjectID a server-owned runtime value. The
	// executor derives it from the one visible NPC actor that matches this
	// contract; the static ObjectID is retained only for compatibility and is
	// never used as a fallback in this mode.
	WindowObjectFromActor bool

	WindowType     int
	WindowSequence int
	WindowObjectID int
}

// NPCWindow and NPCWindowContract are readable aliases for integrations that
// describe the entries as windows or contracts.
type NPCWindow = NPCWindowSpec
type NPCWindowContract = NPCWindowSpec

// NPCSpec is a server-owned, runtime-reviewed NPC contract.  Alias, location,
// visible identity, window identity, choices and prices are all part of the
// contract; an automation request can select one of them but cannot provide
// or replace any of them.
type NPCSpec struct {
	Alias          string
	Floor          int
	X              int
	Y              int
	Name           string
	Template       string
	ActorID        int
	ActorIDKnown   bool
	TalkRange      int
	WindowType     int
	WindowSequence int
	WindowObjectID int
	Choices        map[int]NPCChoice
	// Windows is the preferred multi-window contract. Legacy callers can keep
	// using WindowType/WindowSequence/WindowObjectID/Choices for one window;
	// normalization converts that form to one entry here.
	Windows               []NPCWindowSpec
	WindowObjectFromActor bool
	SourceFingerprint     string
	Verified              bool
	Healer                *HealerRates
}

// NPCChoice is the server-verified meaning of one window selection.  The map
// key in NPCSpec.Choices is the stable choice identifier.  Button defaults to
// that key when omitted, which mirrors the native one-based SELECT buttons.
// Alias/ID are optional names for choices such as "confirm".  Price and Quote
// are compatibility aliases for MaximumCost; if more than one is supplied,
// they must agree.
type NPCChoice struct {
	Button      int
	MaximumCost int64
	Price       int64
	Quote       int64
	Alias       string
	ID          string
	Name        string
	Description string
	Data        string
}

// NewNPCRegistry validates and copies the server-owned entries.  The returned
// map should be retained by the service and never exposed to model input.
func NewNPCRegistry(specs []NPCSpec) (NPCRegistry, error) {
	registry := make(NPCRegistry, len(specs))
	for _, source := range specs {
		spec, alias, err := normalizeNPCSpec(source)
		if err != nil {
			return nil, err
		}
		if _, exists := registry[alias]; exists {
			return nil, fmt.Errorf("%w: duplicate alias %q", ErrNPCRegistry, alias)
		}
		registry[alias] = spec
	}
	return registry, nil
}

// MustNPCRegistry is useful only for process-local static configuration. It
// panics during startup on a malformed catalog instead of silently running a
// partially verified NPC set.
func MustNPCRegistry(specs []NPCSpec) NPCRegistry {
	registry, err := NewNPCRegistry(specs)
	if err != nil {
		panic(err)
	}
	return registry
}

// Lookup returns a validated copy of a catalog entry. A direct map literal is
// accepted for tests and bootstrap code, but invalid entries still fail at
// lookup time; this prevents an unverified direct map from becoming a bypass.
func (r NPCRegistry) Lookup(alias string) (NPCSpec, bool) {
	key := normalizeAlias(alias)
	if key == "" {
		return NPCSpec{}, false
	}
	var found NPCSpec
	foundCount := 0
	for candidate, source := range r {
		if normalizeAlias(candidate) != key {
			continue
		}
		spec, normalized, err := normalizeNPCSpec(source)
		if err != nil || normalized != key {
			return NPCSpec{}, false
		}
		found = spec
		foundCount++
	}
	if foundCount != 1 {
		return NPCSpec{}, false
	}
	return found, true
}

// ObservedWindows returns the verified NPC aliases whose window is the
// currently active server window. Historical entries in Observation.Windows
// are deliberately ignored. An entry is projected only when the observation
// is connected and ready, the active window is open, current and unanswered, and exactly
// one visible actor matches the NPC's verified floor, coordinates and visible
// identity. The returned value is the verified window sequence; an empty map
// means that no alias has been proved by this observation.
//
// This method only projects evidence already present in an observation. It
// does not trust a window sequence, object ID, price, or identity supplied by
// model input.
func (r NPCRegistry) ObservedWindows(observation aimcp.Observation) map[string]int {
	return r.observedWindows(observation, false)
}

// SubmittedWindows is evidence of a successful local write for the current
// verified NPC window. It neither declares server acknowledgement nor makes
// the window actionable again. Replacing the window or reconnecting removes
// this evidence; a prepared checkpoint then needs reconciliation, not replay.
func (r NPCRegistry) SubmittedWindows(observation aimcp.Observation) map[string]int {
	return r.observedWindows(observation, true)
}

func (r NPCRegistry) observedWindows(observation aimcp.Observation, submitted bool) map[string]int {
	result := make(map[string]int)
	if !observation.Connected || !observation.Ready || observation.Battle.Active {
		return result
	}
	if phase := strings.TrimSpace(observation.Phase); phase != "" && !strings.EqualFold(phase, string(aigame.PhaseWorld)) {
		return result
	}
	active := observation.ActiveWindow
	if active == nil || !active.Open || active.Submitted != submitted {
		return result
	}
	// The active pointer is authoritative, but reject an out-of-date pointer
	// when a history list is available. The protocol appends each WN event in
	// order, making the final entry the only current candidate.
	if len(observation.Windows) > 0 {
		latest := observation.Windows[len(observation.Windows)-1]
		if !latest.Open || latest.Submitted != submitted || !sameWindowIdentity(latest, *active) {
			return result
		}
	}
	if active.Sequence <= 0 || active.ObjectID < 0 || active.Type < 0 {
		return result
	}

	matches := 0
	for candidate, source := range r {
		spec, alias, err := normalizeNPCSpec(source)
		if err != nil || normalizeAlias(candidate) != alias {
			continue
		}
		if spec.Floor != observation.Floor {
			continue
		}
		actor, visible := observedNPCActor(spec, observation)
		if !visible {
			continue
		}
		for _, window := range spec.Windows {
			if window.Sequence == active.Sequence && window.Type == active.Type &&
				((window.WindowObjectFromActor && actor.ID == active.ObjectID) ||
					(!window.WindowObjectFromActor && window.ObjectID == active.ObjectID)) {
				// Count matching contracts, including differently named aliases
				// for the same active actor/window. No ambiguous alias wins.
				matches++
				result[alias] = window.Sequence
				break
			}
		}
	}
	if matches != 1 {
		return map[string]int{}
	}
	return result
}

func sameWindowIdentity(left aimcp.WindowState, right aimcp.WindowState) bool {
	return left.Type == right.Type && left.Sequence == right.Sequence && left.ObjectID == right.ObjectID && left.Open == right.Open
}

func observedNPCIdentityUnique(spec NPCSpec, observation aimcp.Observation) bool {
	_, ok := observedNPCActor(spec, observation)
	return ok
}

func observedNPCActor(spec NPCSpec, observation aimcp.Observation) (aimcp.VisibleActor, bool) {
	candidates := 0
	var matched aimcp.VisibleActor
	for _, actor := range observation.Actors {
		if actor.Kind != "" && !strings.EqualFold(strings.TrimSpace(actor.Kind), "character") {
			continue
		}
		if observation.CharacterName != "" && strings.TrimSpace(actor.Name) == strings.TrimSpace(observation.CharacterName) {
			continue
		}
		if observation.Character.Name != "" && strings.TrimSpace(actor.Name) == strings.TrimSpace(observation.Character.Name) {
			continue
		}
		if actor.X != spec.X || actor.Y != spec.Y {
			continue
		}
		if spec.ActorIDKnown && actor.ID != spec.ActorID {
			continue
		}
		if !visibleIdentityMatchesValues(spec.Name, spec.Template, actor.Name, actor.FreeName, actor.Title) {
			continue
		}
		candidates++
		matched = actor
		if candidates > 1 {
			return aimcp.VisibleActor{}, false
		}
	}
	return matched, candidates == 1
}

// NPCSkill is the deterministic executor for the two NPC operations. It
// intentionally does not implement movement: W is owned by MovementSkill.
type NPCSkill struct {
	Backend *GameBackend
	// Registry is the preferred field name. Catalog remains available for
	// service wiring that uses the shorter internal name; Registry wins when
	// both are populated.
	Registry NPCRegistry
	Catalog  NPCRegistry

	// LookConfirmationTimeout bounds the post-L observation. The native
	// session records the submitted L in LastFunction immediately, while a
	// server event may instead advance Revision and replace LastFunction.
	LookConfirmationTimeout time.Duration
}

// NPCSkills is kept as a readable plural alias for service wiring.
type NPCSkills = NPCSkill

// NewNPCSkill constructs an NPC skill with a server-owned catalog.
func NewNPCSkill(backend *GameBackend, catalog NPCRegistry) *NPCSkill {
	return &NPCSkill{Backend: backend, Registry: catalog, Catalog: catalog}
}

// NewNPCSkills is the plural spelling used by some callers.
func NewNPCSkills(backend *GameBackend, catalog NPCRegistry) *NPCSkill {
	return NewNPCSkill(backend, catalog)
}

var (
	ErrNPCRegistry        = errors.New("invalid NPC registry")
	ErrNPCUnknown         = errors.New("unknown NPC alias")
	ErrNPCUnverified      = errors.New("NPC is not runtime verified")
	ErrNPCNotVisible      = errors.New("verified NPC is not visible")
	ErrNPCAmbiguous       = errors.New("verified NPC target is ambiguous")
	ErrNPCOutOfRange      = errors.New("NPC is outside the legal interaction range")
	ErrNPCWrongFacing     = errors.New("player is not facing the NPC")
	ErrNPCWindowUnknown   = errors.New("unknown NPC window")
	ErrNPCWindowInactive  = errors.New("NPC window is not the active server window")
	ErrNPCChoiceUnknown   = errors.New("unknown NPC window choice")
	ErrNPCQuoteUnknown    = errors.New("NPC choice has no verified quote")
	ErrNPCLookUnconfirmed = errors.New("NPC look was not confirmed")
)

type npcTalkArguments struct {
	NPC     string          `json:"npc"`
	Floor   json.RawMessage `json:"floor,omitempty"`
	X       json.RawMessage `json:"x,omitempty"`
	Y       json.RawMessage `json:"y,omitempty"`
	ActorID json.RawMessage `json:"actor_id,omitempty"`
	Command string          `json:"command,omitempty"`
}

type npcWindowArguments struct {
	NPC            string          `json:"npc"`
	WindowType     json.RawMessage `json:"window_type,omitempty"`
	WindowSequence json.RawMessage `json:"window_sequence"`
	WindowObjectID json.RawMessage `json:"window_object_id,omitempty"`
	Choice         json.RawMessage `json:"choice"`
}

func (s *NPCSkill) ValidateSkill(ctx context.Context, action automation.Action) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || s.Backend == nil || s.Backend.Gate == nil || s.Backend.Session == nil {
		return aimcp.ErrBackend
	}
	if action.Skill != "npc.talk" && action.Skill != "npc.window" {
		return fmt.Errorf("%w: unsupported NPC skill %q", aimcp.ErrInvalidParams, action.Skill)
	}
	if action.MaximumCost < 0 {
		return fmt.Errorf("%w: negative maximum cost", aimcp.ErrInvalidParams)
	}
	spec, err := s.lookup(action.Arguments)
	if err != nil {
		return err
	}
	switch action.Skill {
	case "npc.talk":
		args, err := decodeNPCTalk(action.Arguments)
		if err != nil {
			return err
		}
		if action.MaximumCost != 0 {
			return npcInvalid("npc.talk has zero verified cost")
		}
		if err := validateTalkArguments(spec, args); err != nil {
			return err
		}
	case "npc.window":
		args, err := decodeNPCWindow(action.Arguments)
		if err != nil {
			return err
		}
		window, err := resolveNPCWindow(spec, args.WindowSequence)
		if err != nil {
			return err
		}
		choice, err := resolveNPCChoice(window.Choices, args.Choice)
		if err != nil {
			return err
		}
		quote, err := verifiedNPCQuote(choice)
		if err != nil {
			return err
		}
		if action.MaximumCost < quote {
			return npcInvalid("maximum cost %d is below verified quote %d", action.MaximumCost, quote)
		}
		if err := validateWindowArguments(spec, args); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (s *NPCSkill) Execute(ctx context.Context, action automation.Action) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.ValidateSkill(ctx, action); err != nil {
		return err
	}
	if action.ExpectedRevision == 0 {
		return npcInvalid("expected revision is required")
	}
	spec, err := s.lookup(action.Arguments)
	if err != nil {
		return err
	}
	switch action.Skill {
	case "npc.talk":
		args, err := decodeNPCTalk(action.Arguments)
		if err != nil {
			return err
		}
		return s.executeTalk(ctx, action.ExpectedRevision, spec, args)
	case "npc.window":
		args, err := decodeNPCWindow(action.Arguments)
		if err != nil {
			return err
		}
		return s.executeWindow(ctx, action.ExpectedRevision, action.MaximumCost, spec, args)
	default:
		return fmt.Errorf("%w: unsupported NPC skill %q", aimcp.ErrInvalidParams, action.Skill)
	}
}

func (s *NPCSkill) executeTalk(ctx context.Context, expected uint64, spec NPCSpec, args npcTalkArguments) error {
	initial, err := s.observe(ctx)
	if err != nil {
		return err
	}
	if initial.Revision != expected {
		return aigame.ErrStaleRevision
	}
	_, direction, err := validateTalkSnapshot(spec, args, initial)
	if err != nil {
		return err
	}
	if err := s.submit(ctx, expected, func(snapshot aigame.Snapshot) (aigame.Action, error) {
		_, currentDirection, err := validateTalkSnapshot(spec, args, snapshot)
		if err != nil {
			return aigame.Action{}, err
		}
		return aigame.Look(currentDirection), nil
	}); err != nil {
		return err
	}

	// L has no named-protocol response of its own. ExecuteExpected's nil is
	// the packet-boundary confirmation; the follow-up observation verifies that
	// the session still has a live world state and gives TK the newest revision.
	afterLook, err := s.confirmLook(ctx, initial, direction)
	if err != nil {
		return err
	}
	return s.submit(ctx, afterLook.Revision, func(snapshot aigame.Snapshot) (aigame.Action, error) {
		_, currentDirection, err := validateTalkSnapshot(spec, args, snapshot)
		if err != nil {
			return aigame.Action{}, err
		}
		if snapshot.Position.Direction != currentDirection {
			return aigame.Action{}, npcInvalidWith(ErrNPCWrongFacing, "authoritative facing changed before TK")
		}
		return aigame.Talk(snapshot.Position.X, snapshot.Position.Y, "P|hi", 0, 3), nil
	})
}

func (s *NPCSkill) executeWindow(ctx context.Context, expected uint64, maximumCost int64, spec NPCSpec, args npcWindowArguments) error {
	windowSpec, err := resolveNPCWindow(spec, args.WindowSequence)
	if err != nil {
		return err
	}
	choice, err := resolveNPCChoice(windowSpec.Choices, args.Choice)
	if err != nil {
		return err
	}
	quote, err := verifiedNPCQuote(choice)
	if err != nil {
		return err
	}
	if maximumCost < quote {
		return npcInvalid("maximum cost %d is below verified quote %d", maximumCost, quote)
	}
	initial, err := s.observe(ctx)
	if err != nil {
		return err
	}
	if initial.Revision != expected {
		return aigame.ErrStaleRevision
	}
	if err := validateWindowSnapshot(spec, args, initial); err != nil {
		return err
	}
	return s.submit(ctx, expected, func(snapshot aigame.Snapshot) (aigame.Action, error) {
		if err := validateWindowSnapshot(spec, args, snapshot); err != nil {
			return aigame.Action{}, err
		}
		window := snapshot.ActiveWindow
		if window == nil {
			return aigame.Action{}, npcInvalidWith(ErrNPCWindowInactive, "active window disappeared")
		}
		return aigame.Window(snapshot.Position.X, snapshot.Position.Y, window.Sequence, window.ObjectID, int32(choice.Button), choice.Data), nil
	})
}

// submit is the only NPC write path. dispatchGameAction owns the generation
// and mode check while the callback re-observes under that ownership lock for
// raw sessions. Web sessions perform the same final ownership fence inside
// ExecuteExpected, so the helper avoids recursively acquiring the gate.
func (s *NPCSkill) submit(ctx context.Context, expected uint64, build func(aigame.Snapshot) (aigame.Action, error)) error {
	if ctx == nil {
		ctx = context.Background()
	}
	b := s.Backend
	return dispatchGameAction(ctx, b, func(writeCtx context.Context) error {
		snapshot, err := s.observeSession(writeCtx)
		if err != nil {
			return err
		}
		if snapshot.Revision != expected {
			return aigame.ErrStaleRevision
		}
		action, err := build(snapshot)
		if err != nil {
			return err
		}
		bounded, cancel := context.WithTimeout(writeCtx, 3*time.Second)
		defer cancel()
		return b.Session.ExecuteExpected(bounded, expected, action)
	})
}

func (s *NPCSkill) confirmLook(ctx context.Context, before aigame.Snapshot, direction int32) (aigame.Snapshot, error) {
	timeout := s.LookConfirmationTimeout
	if timeout <= 0 || timeout > 2*time.Second {
		timeout = 250 * time.Millisecond
	}
	deadline := time.Now().Add(timeout)
	statusRequested := false
	for {
		after, err := s.observe(ctx)
		if err != nil {
			return aigame.Snapshot{}, err
		}
		if after.Revision < before.Revision {
			return aigame.Snapshot{}, aigame.ErrStaleRevision
		}
		// LastFunction is a local packet-submission marker and a revision
		// advance may belong to an unrelated event. Only the authoritative
		// direction sample proves that the server accepted the turn.
		if after.Position.Direction == direction {
			return after, nil
		}
		if !statusRequested {
			// S:c is a read-only status request, but it still goes through the
			// generation gate and ExpectedRevision fence. It obtains the fresh
			// server position sample used below; a failed request never falls
			// through to TK.
			err = s.submit(ctx, after.Revision, func(snapshot aigame.Snapshot) (aigame.Action, error) {
				if snapshot.Phase != aigame.PhaseWorld || !snapshot.Connected {
					return aigame.Action{}, npcInvalid("NPC look confirmation requires the connected world")
				}
				return aigame.Action{Kind: aigame.ActionStatus, Command: "c"}, nil
			})
			if err == nil {
				statusRequested = true
			} else if !errors.Is(err, aigame.ErrStaleRevision) {
				return aigame.Snapshot{}, err
			}
		}
		if err := ctx.Err(); err != nil {
			return aigame.Snapshot{}, err
		}
		if time.Now().After(deadline) {
			return aigame.Snapshot{}, npcInvalidWith(ErrNPCLookUnconfirmed, "L was submitted without a confirming session state")
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return aigame.Snapshot{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *NPCSkill) observe(ctx context.Context) (aigame.Snapshot, error) {
	if err := s.Backend.check(s.Backend.Binding); err != nil {
		return aigame.Snapshot{}, err
	}
	return s.observeSession(ctx)
}

// observeSession must not call GameBackend.check: submit invokes it while
// Gate.Dispatch holds Gate's mutex.
func (s *NPCSkill) observeSession(ctx context.Context) (aigame.Snapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	b := s.Backend
	if b == nil || b.Session == nil {
		return aigame.Snapshot{}, aimcp.ErrBackend
	}
	snapshot, err := b.Session.Observe(ctx)
	if err != nil {
		return aigame.Snapshot{}, err
	}
	if snapshot.Account != b.Binding.AccountID || snapshot.Character != b.Binding.CharacterName {
		return aigame.Snapshot{}, aimcp.ErrInvalidBinding
	}
	return snapshot, nil
}

func (s *NPCSkill) lookup(arguments json.RawMessage) (NPCSpec, error) {
	var header struct {
		NPC string `json:"npc"`
	}
	if err := json.Unmarshal(arguments, &header); err != nil {
		return NPCSpec{}, aimcp.ErrInvalidParams
	}
	if normalizeAlias(header.NPC) == "" {
		return NPCSpec{}, npcInvalidWith(ErrNPCUnknown, "NPC alias is required")
	}
	spec, ok := s.registry().Lookup(header.NPC)
	if !ok {
		return NPCSpec{}, npcInvalidWith(ErrNPCUnknown, "NPC alias %q is not in the server catalog", header.NPC)
	}
	return spec, nil
}

func (s *NPCSkill) registry() NPCRegistry {
	if s.Registry != nil {
		return s.Registry
	}
	return s.Catalog
}

func decodeNPCTalk(raw json.RawMessage) (npcTalkArguments, error) {
	var args npcTalkArguments
	if err := decodeArguments(raw, &args); err != nil {
		return npcTalkArguments{}, err
	}
	if normalizeAlias(args.NPC) == "" {
		return npcTalkArguments{}, npcInvalidWith(ErrNPCUnknown, "NPC alias is required")
	}
	if args.Command != "" && args.Command != "talk" && args.Command != "hi" && args.Command != "P|hi" {
		return npcTalkArguments{}, npcInvalid("npc.talk command is fixed to P|hi")
	}
	return args, nil
}

func decodeNPCWindow(raw json.RawMessage) (npcWindowArguments, error) {
	var args npcWindowArguments
	if err := decodeArguments(raw, &args); err != nil {
		return npcWindowArguments{}, err
	}
	if normalizeAlias(args.NPC) == "" || len(strings.TrimSpace(string(args.WindowSequence))) == 0 || len(strings.TrimSpace(string(args.Choice))) == 0 {
		return npcWindowArguments{}, npcInvalid("npc.window requires npc, window_sequence and choice")
	}
	if string(args.WindowSequence) == "null" || string(args.Choice) == "null" {
		return npcWindowArguments{}, npcInvalid("npc.window sequence and choice cannot be null")
	}
	return args, nil
}

func validateTalkArguments(spec NPCSpec, args npcTalkArguments) error {
	if normalizeAlias(args.NPC) != normalizeAlias(spec.Alias) {
		return npcInvalidWith(ErrNPCUnknown, "NPC alias does not match the registry entry")
	}
	for _, field := range []struct {
		name     string
		raw      json.RawMessage
		expected int
	}{
		{name: "floor", raw: args.Floor, expected: spec.Floor},
		{name: "x", raw: args.X, expected: spec.X},
		{name: "y", raw: args.Y, expected: spec.Y},
	} {
		value, ok, err := parseOptionalNPCInt(field.raw)
		if err != nil {
			return npcInvalid("invalid %s", field.name)
		}
		if ok && value != int64(field.expected) {
			return npcInvalid("%s does not match the verified NPC location", field.name)
		}
	}
	if _, _, err := parseOptionalNPCInt(args.ActorID); err != nil {
		return npcInvalid("invalid actor_id")
	}
	return nil
}

func validateWindowArguments(spec NPCSpec, args npcWindowArguments) error {
	window, err := resolveNPCWindow(spec, args.WindowSequence)
	if err != nil {
		return err
	}
	if value, ok, err := parseOptionalNPCInt(args.WindowType); err != nil {
		return npcInvalid("invalid window_type")
	} else if ok && value != int64(window.Type) {
		return npcInvalidWith(ErrNPCWindowUnknown, "window type does not match the verified type")
	}
	if value, ok, err := parseOptionalNPCInt(args.WindowObjectID); err != nil {
		return npcInvalid("invalid window_object_id")
	} else if ok && window.WindowObjectFromActor {
		return npcInvalidWith(ErrNPCWindowUnknown, "window object is server-owned for the verified NPC actor")
	} else if ok && value != int64(window.ObjectID) {
		return npcInvalidWith(ErrNPCWindowUnknown, "window object does not match the verified object")
	}
	return nil
}

func validateTalkSnapshot(spec NPCSpec, args npcTalkArguments, snapshot aigame.Snapshot) (aigame.ActorSnapshot, int32, error) {
	if snapshot.Phase != aigame.PhaseWorld || !snapshot.Connected || snapshot.Position.Floor != int32(spec.Floor) {
		return aigame.ActorSnapshot{}, 0, npcInvalid("NPC interaction requires the verified world and floor")
	}
	if err := validateTalkArguments(spec, args); err != nil {
		return aigame.ActorSnapshot{}, 0, err
	}
	actor, err := findNPCActor(spec, args, snapshot)
	if err != nil {
		return aigame.ActorSnapshot{}, 0, err
	}
	rangeLimit := spec.TalkRange
	if rangeLimit == 0 {
		rangeLimit = 1
	}
	distance := max(absInt32(actor.X-snapshot.Position.X), absInt32(actor.Y-snapshot.Position.Y))
	if distance == 0 || int(distance) > rangeLimit {
		return aigame.ActorSnapshot{}, 0, npcInvalidWith(ErrNPCOutOfRange, "distance %d exceeds verified range %d", distance, rangeLimit)
	}
	direction, ok := directionForDelta(actor.X-snapshot.Position.X, actor.Y-snapshot.Position.Y)
	if !ok {
		return aigame.ActorSnapshot{}, 0, npcInvalidWith(ErrNPCWrongFacing, "NPC is not on a legal direction")
	}
	return actor, direction, nil
}

func validateWindowSnapshot(spec NPCSpec, args npcWindowArguments, snapshot aigame.Snapshot) error {
	if snapshot.Phase != aigame.PhaseWorld || !snapshot.Connected || snapshot.Position.Floor != int32(spec.Floor) {
		return npcInvalid("NPC window requires the verified world and floor")
	}
	windowSpec, err := resolveNPCWindow(spec, args.WindowSequence)
	if err != nil {
		return err
	}
	if err := validateWindowArguments(spec, args); err != nil {
		return err
	}
	window := snapshot.ActiveWindow
	if window == nil || !window.Open || window.Submitted {
		return npcInvalidWith(ErrNPCWindowInactive, "no active server window")
	}
	if window.Sequence != int32(windowSpec.Sequence) || window.Type != int32(windowSpec.Type) {
		return npcInvalidWith(ErrNPCWindowInactive, "active sequence/type is not the verified NPC window")
	}
	objectID, err := verifiedWindowObjectID(spec, windowSpec, snapshot)
	if err != nil {
		return err
	}
	if window.ObjectID != objectID {
		return npcInvalidWith(ErrNPCWindowInactive, "active sequence/object is not the verified NPC window")
	}
	return nil
}

func verifiedWindowObjectID(spec NPCSpec, window NPCWindowSpec, snapshot aigame.Snapshot) (int32, error) {
	if !window.WindowObjectFromActor {
		return int32(window.ObjectID), nil
	}
	actor, err := findNPCActor(spec, npcTalkArguments{}, snapshot)
	if err != nil {
		return 0, err
	}
	if actor.ID < 0 {
		return 0, npcInvalidWith(ErrNPCNotVisible, "verified NPC actor has an invalid object identity")
	}
	return actor.ID, nil
}

func findNPCActor(spec NPCSpec, args npcTalkArguments, snapshot aigame.Snapshot) (aigame.ActorSnapshot, error) {
	requestedID, idSet, err := parseOptionalNPCInt(args.ActorID)
	if err != nil {
		return aigame.ActorSnapshot{}, npcInvalid("invalid actor_id")
	}
	if idSet && requestedID < 0 {
		return aigame.ActorSnapshot{}, npcInvalid("actor_id must be non-negative")
	}
	candidates := make([]aigame.ActorSnapshot, 0, 1)
	for _, actor := range snapshot.Actors {
		if actor.Kind != "character" && actor.CharType != 1 {
			continue
		}
		if actor.ID == snapshot.Player.ID && snapshot.Player.ID != 0 {
			continue
		}
		if actor.Name == snapshot.Character {
			continue
		}
		if actor.X != int32(spec.X) || actor.Y != int32(spec.Y) {
			continue
		}
		if spec.ActorIDKnown && actor.ID != int32(spec.ActorID) {
			continue
		}
		if idSet && actor.ID != int32(requestedID) {
			continue
		}
		if !visibleIdentityMatches(spec, actor) {
			continue
		}
		candidates = append(candidates, actor)
	}
	if len(candidates) == 0 {
		return aigame.ActorSnapshot{}, npcInvalidWith(ErrNPCNotVisible, "no visible actor matches the verified NPC")
	}
	if len(candidates) != 1 {
		return aigame.ActorSnapshot{}, npcInvalidWith(ErrNPCAmbiguous, "multiple visible actors match the verified NPC")
	}
	return candidates[0], nil
}

func visibleIdentityMatches(spec NPCSpec, actor aigame.ActorSnapshot) bool {
	return visibleIdentityMatchesValues(spec.Name, spec.Template, actor.Name, actor.FreeName, actor.Title)
}

func visibleIdentityMatchesValues(name, template string, values ...string) bool {
	if name != "" {
		for _, value := range values {
			if strings.TrimSpace(value) == strings.TrimSpace(name) {
				return true
			}
		}
	}
	if template != "" {
		want := strings.ToLower(strings.TrimSpace(template))
		for _, value := range values {
			if strings.ToLower(strings.TrimSpace(value)) == want {
				return true
			}
		}
	}
	return false
}

func resolveNPCWindow(spec NPCSpec, raw json.RawMessage) (NPCWindowSpec, error) {
	sequence, ok, err := parseOptionalNPCInt(raw)
	if err != nil || !ok || sequence <= 0 {
		return NPCWindowSpec{}, npcInvalidWith(ErrNPCWindowUnknown, "window sequence does not match a verified sequence")
	}
	for _, window := range spec.Windows {
		if sequence == int64(window.Sequence) {
			return window, nil
		}
	}
	return NPCWindowSpec{}, npcInvalidWith(ErrNPCWindowUnknown, "window sequence does not match a verified sequence")
}

func resolveNPCChoice(choices map[int]NPCChoice, raw json.RawMessage) (NPCChoice, error) {
	key, label, err := parseChoice(raw)
	if err != nil {
		return NPCChoice{}, npcInvalidWith(ErrNPCChoiceUnknown, "invalid choice")
	}
	if choices == nil {
		return NPCChoice{}, npcInvalidWith(ErrNPCChoiceUnknown, "NPC has no verified choices")
	}
	if key != 0 {
		if choice, ok := choices[int(key)]; ok {
			return normalizeChoice(int(key), choice)
		}
		// A numeric selector may name the native button rather than the
		// stable catalog key; it is still accepted only when registered.
		for id, choice := range choices {
			if choice.Button == int(key) {
				return normalizeChoice(id, choice)
			}
		}
	}
	if label != "" {
		want := strings.ToLower(strings.TrimSpace(label))
		for id, choice := range choices {
			if strings.EqualFold(strings.TrimSpace(choice.Alias), want) || strings.EqualFold(strings.TrimSpace(choice.ID), want) || strings.EqualFold(strings.TrimSpace(choice.Name), want) {
				return normalizeChoice(id, choice)
			}
		}
	}
	return NPCChoice{}, npcInvalidWith(ErrNPCChoiceUnknown, "choice is not registered by the server")
}

func normalizeChoice(id int, choice NPCChoice) (NPCChoice, error) {
	if id <= 0 {
		return NPCChoice{}, npcInvalidWith(ErrNPCChoiceUnknown, "choice identifier must be positive")
	}
	if choice.Button == 0 {
		choice.Button = id
	}
	if choice.Button <= 0 {
		return NPCChoice{}, npcInvalidWith(ErrNPCChoiceUnknown, "choice button must be positive")
	}
	if _, err := verifiedNPCQuote(choice); err != nil {
		return NPCChoice{}, err
	}
	return choice, nil
}

func verifiedNPCQuote(choice NPCChoice) (int64, error) {
	values := []int64{choice.MaximumCost, choice.Price, choice.Quote}
	quote := int64(0)
	for _, value := range values {
		if value < 0 {
			return 0, npcInvalidWith(ErrNPCQuoteUnknown, "negative verified quote")
		}
		if value == 0 {
			continue
		}
		if quote != 0 && quote != value {
			return 0, npcInvalidWith(ErrNPCQuoteUnknown, "conflicting verified quotes")
		}
		quote = value
	}
	// A zero quote is a valid verified free action. The registry itself is
	// the evidence that this value is known; request fields cannot establish
	// that fact.
	return quote, nil
}

func normalizeNPCSpec(source NPCSpec) (NPCSpec, string, error) {
	if source.Healer != nil {
		rates := *source.Healer
		if err := rates.validate(); err != nil {
			return NPCSpec{}, "", err
		}
		source.Healer = &rates
	}
	alias := normalizeAlias(source.Alias)
	if alias == "" {
		return NPCSpec{}, "", fmt.Errorf("%w: alias is required", ErrNPCRegistry)
	}
	if !source.Verified {
		return NPCSpec{}, "", fmt.Errorf("%w: %s", ErrNPCUnverified, alias)
	}
	if strings.TrimSpace(source.SourceFingerprint) == "" || strings.IndexFunc(source.SourceFingerprint, func(r rune) bool { return r == '\r' || r == '\n' || r == '\t' }) >= 0 {
		return NPCSpec{}, "", fmt.Errorf("%w: source fingerprint is required", ErrNPCRegistry)
	}
	if source.Floor < 0 || source.X < 0 || source.Y < 0 {
		return NPCSpec{}, "", fmt.Errorf("%w: %s has invalid coordinates", ErrNPCRegistry, alias)
	}
	if strings.TrimSpace(source.Name) == "" && strings.TrimSpace(source.Template) == "" {
		return NPCSpec{}, "", fmt.Errorf("%w: %s needs a visible name or template", ErrNPCRegistry, alias)
	}
	if source.TalkRange < 0 || source.TalkRange > 8 {
		return NPCSpec{}, "", fmt.Errorf("%w: %s has invalid talk range", ErrNPCRegistry, alias)
	}
	if !source.ActorIDKnown {
		source.ActorID = 0
	} else if source.ActorID < 0 {
		return NPCSpec{}, "", fmt.Errorf("%w: %s has invalid actor id", ErrNPCRegistry, alias)
	}
	legacyWindowPresent := source.WindowType != 0 || source.WindowSequence != 0 || source.WindowObjectID != 0 || len(source.Choices) != 0
	rawWindows := append([]NPCWindowSpec(nil), source.Windows...)
	if legacyWindowPresent {
		rawWindows = append(rawWindows, NPCWindowSpec{Type: source.WindowType, Sequence: source.WindowSequence, ObjectID: source.WindowObjectID, Choices: source.Choices, WindowObjectFromActor: source.WindowObjectFromActor})
	}
	if source.WindowObjectFromActor {
		for index := range rawWindows {
			rawWindows[index].WindowObjectFromActor = true
		}
	}
	windows := make([]NPCWindowSpec, 0, len(rawWindows))
	for _, rawWindow := range rawWindows {
		window, err := normalizeNPCWindow(rawWindow)
		if err != nil {
			return NPCSpec{}, "", fmt.Errorf("%w: %s window: %v", ErrNPCRegistry, alias, err)
		}
		duplicate := false
		for _, previous := range windows {
			if previous.Sequence != window.Sequence {
				continue
			}
			if previous.Type == window.Type && previous.ObjectID == window.ObjectID && previous.WindowObjectFromActor == window.WindowObjectFromActor && sameNPCChoices(previous.Choices, window.Choices) {
				duplicate = true
				break
			}
			return NPCSpec{}, "", fmt.Errorf("%w: %s has duplicate or conflicting window sequence %d", ErrNPCRegistry, alias, window.Sequence)
		}
		if !duplicate {
			windows = append(windows, window)
		}
	}
	source.Alias = alias
	source.Name = strings.TrimSpace(source.Name)
	source.Template = strings.TrimSpace(source.Template)
	source.SourceFingerprint = strings.TrimSpace(source.SourceFingerprint)
	source.Windows = windows
	if legacyWindowPresent {
		for _, window := range windows {
			if window.Sequence == source.WindowSequence {
				source.WindowType = window.Type
				source.WindowSequence = window.Sequence
				source.WindowObjectID = window.ObjectID
				source.Choices = cloneNPCChoices(window.Choices)
				break
			}
		}
	} else if source.Choices != nil {
		// A choices map without a legacy identity is an incomplete window;
		// this branch is unreachable because legacyWindowPresent includes it,
		// but keep the copy rule explicit for future additions.
		source.Choices = cloneNPCChoices(source.Choices)
	}
	return source, alias, nil
}

func normalizeNPCWindow(source NPCWindowSpec) (NPCWindowSpec, error) {
	typ, err := chooseWindowIdentity(source.Type, source.WindowType, "type")
	if err != nil {
		return NPCWindowSpec{}, err
	}
	sequence, err := chooseWindowIdentity(source.Sequence, source.WindowSequence, "sequence")
	if err != nil {
		return NPCWindowSpec{}, err
	}
	objectID, err := chooseWindowIdentity(source.ObjectID, source.WindowObjectID, "object id")
	if err != nil {
		return NPCWindowSpec{}, err
	}
	if typ < 0 || sequence <= 0 || objectID < 0 {
		return NPCWindowSpec{}, errors.New("invalid window identity")
	}
	if len(source.Choices) == 0 {
		return NPCWindowSpec{}, errors.New("window has no verified choices")
	}
	choices := cloneNPCChoices(source.Choices)
	for id, choice := range choices {
		normalized, err := normalizeChoice(id, choice)
		if err != nil {
			return NPCWindowSpec{}, fmt.Errorf("choice %d: %v", id, err)
		}
		choices[id] = normalized
	}
	return NPCWindowSpec{Type: typ, Sequence: sequence, ObjectID: objectID, Choices: choices,
		WindowObjectFromActor: source.WindowObjectFromActor,
		WindowType:            typ, WindowSequence: sequence, WindowObjectID: objectID}, nil
}

func chooseWindowIdentity(preferred, alias int, name string) (int, error) {
	if preferred != 0 && alias != 0 && preferred != alias {
		return 0, fmt.Errorf("conflicting window %s", name)
	}
	if preferred != 0 {
		return preferred, nil
	}
	return alias, nil
}

func cloneNPCChoices(source map[int]NPCChoice) map[int]NPCChoice {
	if source == nil {
		return nil
	}
	clone := make(map[int]NPCChoice, len(source))
	for id, choice := range source {
		clone[id] = choice
	}
	return clone
}

func sameNPCChoices(left, right map[int]NPCChoice) bool {
	if len(left) != len(right) {
		return false
	}
	for id, choice := range left {
		if other, ok := right[id]; !ok || other != choice {
			return false
		}
	}
	return true
}

func parseOptionalNPCInt(raw json.RawMessage) (int64, bool, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return 0, false, nil
	}
	if trimmed == "null" {
		return 0, false, errors.New("null integer")
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil || strings.TrimSpace(text) == "" {
			return 0, false, errors.New("invalid integer string")
		}
		value, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
		return value, true, err
	}
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return 0, false, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return 0, false, errors.New("trailing integer data")
	}
	number, ok := value.(json.Number)
	if !ok {
		return 0, false, errors.New("integer required")
	}
	parsed, err := strconv.ParseInt(string(number), 10, 64)
	return parsed, true, err
}

func parseChoice(raw json.RawMessage) (int64, string, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return 0, "", errors.New("choice is empty")
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil || strings.TrimSpace(text) == "" {
			return 0, "", errors.New("invalid choice string")
		}
		text = strings.TrimSpace(text)
		if number, err := strconv.ParseInt(text, 10, 64); err == nil {
			return number, "", nil
		}
		return 0, text, nil
	}
	number, ok, err := parseOptionalNPCInt(raw)
	if err != nil || !ok {
		return 0, "", errors.New("choice must be an integer or name")
	}
	return number, "", nil
}

func normalizeAlias(alias string) string {
	return strings.ToLower(strings.TrimSpace(alias))
}

func npcInvalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", aimcp.ErrInvalidParams, fmt.Sprintf(format, args...))
}

func npcInvalidWith(kind error, format string, args ...any) error {
	return fmt.Errorf("%w: %w: %s", aimcp.ErrInvalidParams, kind, fmt.Sprintf(format, args...))
}

func absInt32(value int32) int32 {
	if value < 0 {
		return -value
	}
	return value
}

func max(a, b int32) int32 {
	if a > b {
		return a
	}
	return b
}

func directionForDelta(dx, dy int32) (int32, bool) {
	if dx > 1 {
		dx = 1
	} else if dx < -1 {
		dx = -1
	}
	if dy > 1 {
		dy = 1
	} else if dy < -1 {
		dy = -1
	}
	for direction, delta := range [...]struct{ x, y int32 }{
		{0, -1}, {1, -1}, {1, 0}, {1, 1}, {0, 1}, {-1, 1}, {-1, 0}, {-1, -1},
	} {
		if delta.x == dx && delta.y == dy {
			return int32(direction), true
		}
	}
	return 0, false
}

var _ DeterministicSkill = (*NPCSkill)(nil)
