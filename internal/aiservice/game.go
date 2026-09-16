package aiservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
	"github.com/k0ngk0ng/stoneage/internal/characterbuild"
)

// GameSession is implemented by the authenticated headless protocol session.
// ExecuteExpected checks the revision at the protocol submission boundary.
type GameSession interface {
	Observe(context.Context) (aigame.Snapshot, error)
	ExecuteExpected(context.Context, uint64, aigame.Action) error
}

// controlGateSession marks a session whose ExecuteExpected implementation
// already fences the write through the character control gate. The Web
// adapter uses this marker because wrapping its write in another
// Gate.Dispatch would try to acquire Gate.mu recursively while the outer
// dispatch is still holding it.
type controlGateSession interface {
	UsesControlGate()
}

// FundingLookup is a server-owned capability lookup. It is evaluated for
// every observation so revoking an AI funding policy is visible to the
// automation layer without restarting the game session.
type FundingLookup func(context.Context) (bool, error)

type TaskController interface {
	StartTask(context.Context, aimcp.TaskRequest) (aimcp.TaskReceipt, error)
	StartLeveling(context.Context, aimcp.LevelingRequest) (aimcp.TaskReceipt, error)
	Status(context.Context, string) (aimcp.TaskReceipt, error)
	Cancel(context.Context, aimcp.CancelRequest) (aimcp.TaskReceipt, error)
}

func (b *GameBackend) Active(ctx context.Context) ([]aimcp.TaskReceipt, error) {
	if err := b.check(b.Binding); err != nil {
		return nil, err
	}
	if tasks, ok := b.Tasks.(interface {
		Active(context.Context) ([]aimcp.TaskReceipt, error)
	}); ok {
		return tasks.Active(ctx)
	}
	return nil, nil
}

func (b *GameBackend) PendingActions(ctx context.Context) ([]aimcp.TaskReceipt, error) {
	if err := b.check(b.Binding); err != nil {
		return nil, err
	}
	if b.Receipts == nil {
		return nil, nil
	}
	return b.Receipts.Unknown(ctx, b.Binding)
}

// GameBackend binds every operation to the same authenticated character and
// ownership generation. Its fields are configured before it is registered.
type GameBackend struct {
	stockOffers     map[string]StockContract
	Binding         aimcp.Binding
	Gate            *aicontrol.Gate
	Owner           aicontrol.Mode
	Session         GameSession
	Funding         FundingLookup
	Knowledge       *aiknowledge.Knowledge
	Tasks           TaskController
	Receipts        *ReceiptStore
	Schedules       *airuntime.Store
	AgentNotes      *airuntime.Store
	OwnStateRefresh *OwnStateRefresher
	CharacterBuild  *characterbuild.Policy
	statMu          sync.Mutex
	statAllocations map[string]statAllocationEvidence
}

// CreateSchedule persists a bounded wake-up for this exact AI profile. The
// capability is checked with the same owner/generation fence as game reads;
// schedule creation cannot be used after human takeover or session revoke.
func (b *GameBackend) CreateSchedule(ctx context.Context, binding aimcp.Binding, request aimcp.ScheduleRequest) (aimcp.Schedule, error) {
	if err := b.check(binding); err != nil {
		return aimcp.Schedule{}, err
	}
	if b.Schedules == nil || strings.TrimSpace(binding.ProfileID) == "" {
		return aimcp.Schedule{}, aimcp.ErrBackend
	}
	input, err := scheduleInput(request)
	if err != nil {
		return aimcp.Schedule{}, err
	}
	schedule, err := b.Schedules.CreateSchedule(ctx, binding.ProfileID, input)
	if err != nil {
		return aimcp.Schedule{}, err
	}
	return projectSchedule(schedule), nil
}

func (b *GameBackend) ListSchedules(ctx context.Context, binding aimcp.Binding, request aimcp.ScheduleListRequest) (aimcp.ScheduleList, error) {
	if err := b.check(binding); err != nil {
		return aimcp.ScheduleList{}, err
	}
	if b.Schedules == nil || strings.TrimSpace(binding.ProfileID) == "" {
		return aimcp.ScheduleList{}, aimcp.ErrBackend
	}
	schedules, err := b.Schedules.ListSchedules(ctx, binding.ProfileID, request.Limit)
	if err != nil {
		return aimcp.ScheduleList{}, err
	}
	result := aimcp.ScheduleList{Schedules: make([]aimcp.Schedule, 0, len(schedules))}
	for _, schedule := range schedules {
		if request.Status != "" && string(schedule.Status) != request.Status {
			continue
		}
		result.Schedules = append(result.Schedules, projectSchedule(schedule))
	}
	return result, nil
}

func (b *GameBackend) CancelSchedule(ctx context.Context, binding aimcp.Binding, request aimcp.ScheduleCancelRequest) (aimcp.Schedule, error) {
	if err := b.check(binding); err != nil {
		return aimcp.Schedule{}, err
	}
	if b.Schedules == nil || strings.TrimSpace(binding.ProfileID) == "" {
		return aimcp.Schedule{}, aimcp.ErrBackend
	}
	schedule, err := b.Schedules.CancelSchedule(ctx, binding.ProfileID, request.ScheduleID, request.Reason)
	if err != nil {
		return aimcp.Schedule{}, err
	}
	return projectSchedule(schedule), nil
}

func scheduleInput(request aimcp.ScheduleRequest) (airuntime.ScheduleInput, error) {
	input := airuntime.ScheduleInput{Kind: request.Kind, Title: request.Title, Prompt: request.Prompt,
		DelaySeconds: request.DelaySeconds, RepeatSeconds: request.RepeatSeconds,
		IdempotencyKey: request.IdempotencyKey}
	if strings.TrimSpace(request.RunAt) != "" {
		runAt, err := time.Parse(time.RFC3339Nano, request.RunAt)
		if err != nil {
			return airuntime.ScheduleInput{}, aimcp.ErrInvalidParams
		}
		input.RunAt = runAt
	}
	return input, nil
}

func projectSchedule(schedule airuntime.Schedule) aimcp.Schedule {
	return aimcp.Schedule{ID: schedule.ID, Kind: schedule.Kind, Title: schedule.Title, Prompt: schedule.Prompt,
		RunAt: schedule.RunAt.UTC().Format(time.RFC3339Nano), RepeatSeconds: int64(schedule.RepeatInterval / time.Second),
		Status: string(schedule.Status), Occurrences: schedule.Occurrences, LastOutcome: cloneRawJSON(schedule.LastOutcome)}
}

func cloneRawJSON(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

// dispatchGameAction serializes one game action with the ownership gate. Most
// sessions are raw protocol sessions and therefore need the outer dispatch.
// A Web AutomationSession already performs that dispatch in ExecuteExpected;
// for it, validate the current lease here and let the session fence the
// actual write. The callback must remain bounded and must never retry a
// non-idempotent action.
func dispatchGameAction(ctx context.Context, backend *GameBackend, send func(context.Context) error) error {
	if backend == nil || backend.Gate == nil || backend.Session == nil {
		return aimcp.ErrBackend
	}
	if send == nil {
		return errors.New("missing game action")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, alreadyFenced := backend.Session.(controlGateSession); alreadyFenced {
		if err := backend.check(backend.Binding); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return send(ctx)
	}
	return backend.Gate.Dispatch(ctx, backend.Binding.Generation, backend.Owner, send)
}

func (b *GameBackend) check(binding aimcp.Binding) error {
	if binding != b.Binding || b.Gate == nil || b.Session == nil {
		return aimcp.ErrInvalidBinding
	}
	s := b.Gate.State()
	if s.Generation != binding.Generation {
		return aicontrol.ErrStale
	}
	if s.Mode != b.Owner {
		return aicontrol.ErrOwner
	}
	return nil
}

func (b *GameBackend) Observe(ctx context.Context, binding aimcp.Binding) (aimcp.Observation, error) {
	return b.observeWithReceipt(ctx, binding, "")
}

func (b *GameBackend) observeWithReceipt(ctx context.Context, binding aimcp.Binding, receiptHandle string) (aimcp.Observation, error) {
	if err := b.check(binding); err != nil {
		return aimcp.Observation{}, err
	}
	s, err := b.Session.Observe(ctx)
	if err != nil {
		return aimcp.Observation{}, err
	}
	if s.Account != binding.AccountID || s.Character != binding.CharacterName {
		return aimcp.Observation{}, aimcp.ErrInvalidBinding
	}
	if b.OwnStateRefresh != nil {
		s, err = b.OwnStateRefresh.Refresh(ctx, b, s)
		if err != nil {
			return aimcp.Observation{}, err
		}
	}
	if s.Account != binding.AccountID || s.Character != binding.CharacterName {
		return aimcp.Observation{}, aimcp.ErrInvalidBinding
	}
	if err := b.reconcileMailListActions(ctx, binding, s, receiptHandle); err != nil {
		return aimcp.Observation{}, err
	}
	o := ProjectObservation(binding, s)
	if err := b.reconcileStatAllocations(ctx, s); err != nil {
		return aimcp.Observation{}, err
	}
	if err := b.projectCharacterBuild(ctx, &o, s); err != nil {
		return aimcp.Observation{}, err
	}
	if b.Funding != nil {
		o.UnlimitedFunds, err = b.Funding(ctx)
		if err != nil {
			return aimcp.Observation{}, err
		}
	}
	return o, nil
}

// ProjectObservation omits raw protocol fields and account data. Missing
// durable pet identities remain empty rather than borrowing a reusable slot.
func ProjectObservation(binding aimcp.Binding, s aigame.Snapshot) aimcp.Observation {
	o := aimcp.Observation{Revision: s.Revision, CharacterID: binding.CharacterID,
		CharacterName: s.Character, Connected: s.Connected,
		Ready: s.Player.HasStatus && (s.Phase == aigame.PhaseWorld || s.Phase == aigame.PhaseBattle),
		Phase: string(s.Phase), Floor: int(s.Position.Floor), X: int(s.Position.X), Y: int(s.Position.Y), Gold: int64(s.Player.Gold),
		Character: aimcp.Entity{ID: binding.CharacterID, Name: s.Player.Name, Level: int(s.Player.Level), HP: int(s.Player.HP), MaxHP: int(s.Player.MaxHP), Alive: s.Player.HasStatus && s.Player.HP > 0},
		Inventory: map[string]int{}, Flags: map[string]bool{}, Skills: map[string]int{}, OwnProgress: map[string]int{},
		Battle: aimcp.BattleState{Active: s.Battle.Active, Turn: int(s.Battle.Turn), CommandReady: s.Battle.CommandReady, Result: s.Battle.Result,
			MyNo: int(s.Battle.MyNo), MyNoKnown: s.Battle.MyNoKnown, BPFlags: int(s.Battle.BPFlags), PlayerCommandReady: s.Battle.PlayerCommandReady(), PetCommandReady: s.Battle.PetCommandReady()},
	}
	if s.Player.StatPointsKnown {
		o.Flags["stat_points:known"] = true
		o.OwnProgress["stat_points"] = int(s.Player.UnspentStatPoints)
	}
	o.Trade = projectTrade(s.Trade)
	o.RidingPet = aimcp.PetSelection{Known: s.Player.RidePetKnown, Slot: int(s.Player.RidePet)}
	o.BattlePet = aimcp.PetSelection{Known: s.Player.BattlePetSlotKnown, Slot: int(s.Player.BattlePetSlot)}
	if s.Player.SocialFlagsKnown {
		o.Flags["social:known"] = true
		for name, mask := range map[string]int32{"party": 1, "duel": 4, "party-chat": 8, "trade-card": 16, "trade": 32} {
			o.Flags["social:"+name] = s.Player.SocialFlags&mask != 0
		}
	}
	// Zero may mean an attribute has never arrived in a partial status stream.
	// Expose positive observed attributes without inventing missing values.
	if s.Player.HasStatus {
		for name, value := range map[string]int32{"vital": s.Player.Vital, "strength": s.Player.Strength, "toughness": s.Player.Toughness, "dexterity": s.Player.Dexterity} {
			if value > 0 {
				o.OwnProgress["attribute_"+name] = int(value)
			}
		}
	}
	// Character skill IDs are the stable server values carried by the P1
	// status stream. Keep the decimal ID as the key: display names belong to a
	// versioned catalog and must not be guessed at the observation boundary.
	for _, skill := range s.Skills {
		o.Skills[strconv.Itoa(int(skill.ID))] = int(skill.Level)
	}
	// S("AI") is optional. Only a received, validated response contributes
	// character-scoped progress or riding permission; zero values are still
	// meaningful and therefore retained when the response was received.
	if s.AI.Received {
		if s.Connected && o.Ready && aigame.ValidPersistentCharacterID(s.AI.PersistentCharacterID) {
			o.PersistentCharacterID = s.AI.PersistentCharacterID
		}
		if s.Connected && s.Phase == aigame.PhaseWorld && s.Player.HasStatus && s.AI.PartyModeKnown {
			o.Flags["party:known"] = true
			o.Flags["party:solo"] = s.AI.PartyMode == 0
			o.OwnProgress["party_mode"] = int(s.AI.PartyMode)
		}
		for index, value := range s.AI.EndEvents {
			key := fmt.Sprintf("end_event_%d", index)
			o.OwnProgress[key] = int(value)
			o.Flags[key] = value != 0
			for bit := 0; bit < 32; bit++ {
				o.Flags[fmt.Sprintf("end:%d", index*32+bit)] = uint32(value)&(uint32(1)<<bit) != 0
			}
		}
		for index, value := range s.AI.NowEvents {
			key := fmt.Sprintf("now_event_%d", index)
			o.OwnProgress[key] = int(value)
			o.Flags[key] = value != 0
			for bit := 0; bit < 32; bit++ {
				o.Flags[fmt.Sprintf("now:%d", index*32+bit)] = uint32(value)&(uint32(1)<<bit) != 0
			}
		}
		o.Skills["learn_ride"] = int(s.AI.LearnRide)
		if s.AI.SavePointsKnown {
			o.OwnProgress["savepoints"] = int(s.AI.SavePoints)
			for bit := 0; bit < 32; bit++ {
				o.Flags[fmt.Sprintf("savepoint:%d", bit)] = uint32(s.AI.SavePoints)&(uint32(1)<<bit) != 0
			}
		}
		if s.AI.ItemsKnown {
			o.Flags["inventory:known"] = true
			o.OwnProgress["backpack_used_slots"] = len(s.AI.Items)
			for _, item := range s.AI.Items {
				o.Inventory["item:"+strconv.Itoa(int(item.TemplateID))]++
			}
		}
	}
	for _, p := range s.Pets {
		id := ""
		if p.IdentityKnown && strings.TrimSpace(p.StableID) != "" {
			id = p.StableID
		}
		entity := aimcp.Entity{ID: id, Name: p.Name, Level: int(p.Level), HP: int(p.HP), MaxHP: int(p.MaxHP), Alive: p.Alive, UseFlag: int(p.UseFlag)}
		for _, skill := range p.Skills {
			if skill.ID != 0 {
				entity.Skills = append(entity.Skills, strconv.Itoa(int(skill.ID)))
			}
		}
		o.Pets = append(o.Pets, entity)
		if o.RidingPet.Known && o.RidingPet.Slot == int(p.Slot) {
			copy := entity
			o.RidingPet.Pet = &copy
			o.RidingPet.LocalIdentity, o.RidingPet.LocalEpoch = p.Identity, p.IdentityEpoch
		}
		if o.BattlePet.Known && o.BattlePet.Slot == int(p.Slot) {
			copy := entity
			o.BattlePet.Pet = &copy
			o.BattlePet.LocalIdentity, o.BattlePet.LocalEpoch = p.Identity, p.IdentityEpoch
		}
	}
	for _, p := range s.Party {
		o.Party = append(o.Party, aimcp.PartyMember{PersistentCharacterID: p.PersistentCharacterID, ID: strconv.Itoa(int(p.ID)), Name: p.Name, Level: int(p.Level), HP: int(p.HP), MaxHP: int(p.MaxHP)})
	}
	for _, a := range s.Actors {
		o.Actors = append(o.Actors, aimcp.VisibleActor{PersistentCharacterID: a.PersistentCharacterID, ID: int(a.ID), Kind: a.Kind, CharType: int(a.CharType), Name: a.Name, FreeName: a.FreeName, Title: a.Title, X: int(a.X), Y: int(a.Y), Direction: int(a.Direction), Level: int(a.Level), PetName: a.PetName, PetLevel: int(a.PetLevel)})
	}
	for _, i := range s.Inventory {
		o.InventoryItems = append(o.InventoryItems, aimcp.InventoryItem{Index: int(i.Index), Name: i.Name, Name2: i.Name2, Memo: i.Memo, Graphic: int(i.Graphic), Count: 1})
		// Stock inventory has individual occupied slots, not stack counts.
		// Reserve item:<id> for server template identities. A display name
		// must not masquerade as a template-based quest requirement.
		if !strings.HasPrefix(i.Name, "item:") {
			o.Inventory[i.Name]++
		}
	}
	window := func(w aigame.WindowSnapshot) aimcp.WindowState {
		return aimcp.WindowState{Type: int(w.Type), ButtonType: int(w.ButtonType), Sequence: int(w.Sequence), ObjectID: int(w.ObjectID), Data: w.Data, Open: w.Open, Submitted: w.Submitted}
	}
	for _, w := range s.Windows {
		o.Windows = append(o.Windows, window(w))
	}
	if s.ActiveWindow != nil {
		w := window(*s.ActiveWindow)
		o.ActiveWindow = &w
	}
	for _, c := range s.Chat {
		o.Chat = append(o.Chat, aimcp.ChatMessage{SpeakerCharacterID: c.SpeakerCharacterID, ContextID: c.ContextID, At: c.At.UTC().Format(time.RFC3339Nano), Channel: c.Channel, FromID: int(c.FromID), Color: int(c.Color), Text: c.Text})
	}
	o.AddressBookKnown = s.AddressBookKnown
	o.AddressBookRevision = s.AddressBookRevision
	for _, entry := range s.AddressBook {
		o.AddressBook = append(o.AddressBook, aimcp.AddressBookEntry{Index: int(entry.Index), Use: entry.Use, Online: entry.Online, Level: int(entry.Level), DuelPoint: int(entry.DuelPoint), Graphic: int(entry.Graphic), Name: entry.Name, Transmigration: int(entry.Transmigration)})
	}
	for _, p := range s.Battle.Participants {
		o.Battle.Participants = append(o.Battle.Participants, aimcp.BattleParticipant{ID: int(p.BattleID), Name: p.Name, Level: int(p.Level), HP: int(p.HP), MaxHP: int(p.MaxHP), Player: p.Player, Dead: p.Dead})
	}
	return o
}

func (b *GameBackend) QueryKnowledge(ctx context.Context, binding aimcp.Binding, q aimcp.KnowledgeQuery) (aimcp.KnowledgeResult, error) {
	if err := b.check(binding); err != nil {
		return aimcp.KnowledgeResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return aimcp.KnowledgeResult{}, err
	}
	if b.Knowledge == nil {
		return aimcp.KnowledgeResult{}, aimcp.ErrBackend
	}
	r := aimcp.KnowledgeResult{Kind: q.Kind, Revision: b.Knowledge.Fingerprint(), Entries: []aimcp.KnowledgeEntry{}}
	limit := q.Limit
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	add := func(id, title string, verified bool, v any) {
		if len(r.Entries) >= limit || (q.ID != "" && q.ID != id) || (q.Text != "" && !strings.Contains(strings.ToLower(id+" "+title), strings.ToLower(q.Text))) {
			return
		}
		raw, _ := json.Marshal(v)
		r.Entries = append(r.Entries, aimcp.KnowledgeEntry{ID: id, Title: title, Verified: verified, Data: raw})
	}
	switch q.Kind {
	case "task":
		for _, t := range b.Knowledge.Tasks() {
			order, err := b.Knowledge.TaskOrder(t.ID)
			verified := err == nil
			for _, dependency := range order {
				verified = verified && dependency.Status == aiknowledge.TaskVerified && dependency.EvidenceVerified && dependency.ExecutionVerified && dependency.PreparationReviewed && strings.TrimSpace(dependency.PreparationNotes) != ""
			}
			add(t.ID, t.Name, verified, t)
		}
	case "leveling":
		for _, a := range b.Knowledge.Areas() {
			// This flag attests encounter facts, not an approach route. The
			// leveling navigator separately validates reachability and every
			// effective encounter along the character's actual approach.
			add(strconv.Itoa(a.ID), "encounter area facts (approach validated at execution)", a.Verified, a)
		}
	case "facts", "rule":
		add("coverage", "Knowledge coverage", true, b.Knowledge.Coverage())
		if q.Kind == "rule" {
			for _, offer := range b.stockKnowledge() {
				add("supply:"+offer.Alias, "reviewed supply offer (travel validated at execution)", true, offer)
			}
		}
	case "route":
		for i, w := range b.Knowledge.Warps {
			add(strconv.Itoa(i), "warp data (tile route not included)", false, w)
		}
	default:
		return r, aimcp.ErrInvalidParams
	}
	return r, nil
}

func (b *GameBackend) StartTask(ctx context.Context, binding aimcp.Binding, q aimcp.TaskRequest) (aimcp.TaskReceipt, error) {
	if err := b.check(binding); err != nil {
		return aimcp.TaskReceipt{}, err
	}
	if b.Tasks == nil {
		return aimcp.TaskReceipt{}, aimcp.ErrBackend
	}
	return b.Tasks.StartTask(ctx, q)
}
func (b *GameBackend) StartLeveling(ctx context.Context, binding aimcp.Binding, q aimcp.LevelingRequest) (aimcp.TaskReceipt, error) {
	if err := b.check(binding); err != nil {
		return aimcp.TaskReceipt{}, err
	}
	if b.Tasks == nil {
		return aimcp.TaskReceipt{}, aimcp.ErrBackend
	}
	return b.Tasks.StartLeveling(ctx, q)
}
func (b *GameBackend) TaskStatus(ctx context.Context, binding aimcp.Binding, id string) (aimcp.TaskReceipt, error) {
	if err := b.check(binding); err != nil {
		return aimcp.TaskReceipt{}, err
	}
	if strings.HasPrefix(id, "action-") {
		if b.Receipts == nil {
			return aimcp.TaskReceipt{}, aimcp.ErrBackend
		}
		action, receipt, err := b.Receipts.LoadAction(ctx, binding, id)
		if err != nil {
			return aimcp.TaskReceipt{}, err
		}
		if receipt.Status == aimcp.ReceiptUnknown && isMailListAction(action) {
			// Observe performs reconciliation against the current snapshot. It
			// never resubmits AB, so polling a mail/list receipt cannot issue a
			// second query.
			if _, err := b.observeWithReceipt(ctx, binding, id); err != nil {
				return aimcp.TaskReceipt{}, err
			}
			return b.Receipts.Load(ctx, binding, id)
		}
		b.statMu.Lock()
		_, reconcile := b.statAllocations[id]
		b.statMu.Unlock()
		if reconcile {
			if _, err := b.Observe(ctx, binding); err != nil {
				return aimcp.TaskReceipt{}, err
			}
		}
		return b.Receipts.Load(ctx, binding, id)
	}
	if b.Tasks == nil {
		return aimcp.TaskReceipt{}, aimcp.ErrBackend
	}
	return b.Tasks.Status(ctx, id)
}
func (b *GameBackend) Cancel(ctx context.Context, binding aimcp.Binding, q aimcp.CancelRequest) (aimcp.TaskReceipt, error) {
	if err := b.check(binding); err != nil {
		return aimcp.TaskReceipt{}, err
	}
	if strings.HasPrefix(q.Handle, "action-") {
		return aimcp.TaskReceipt{}, errors.New("submitted game actions cannot be cancelled; observe their outcome")
	}
	if b.Tasks == nil {
		return aimcp.TaskReceipt{}, aimcp.ErrBackend
	}
	return b.Tasks.Cancel(ctx, q)
}

func (b *GameBackend) GameAction(ctx context.Context, binding aimcp.Binding, a aimcp.TypedAction) (aimcp.ActionReceipt, error) {
	return b.gameAction(ctx, binding, a, false)
}

// preparation is server-owned and never part of the MCP action schema. Only
// the leveling idle hook may allocate while a task is still running.
func (b *GameBackend) gameAction(ctx context.Context, binding aimcp.Binding, a aimcp.TypedAction, preparation bool) (aimcp.ActionReceipt, error) {
	if b == nil {
		return aimcp.ActionReceipt{}, aimcp.ErrBackend
	}
	if a.Kind == "allocate-stat" {
		b.statMu.Lock()
		defer b.statMu.Unlock()
	}
	if err := b.check(binding); err != nil {
		return aimcp.ActionReceipt{}, err
	}
	if b.Receipts == nil {
		return aimcp.ActionReceipt{}, aimcp.ErrBackend
	}
	if a.Kind == "allocate-stat" && !preparation {
		active, err := b.Active(ctx)
		if err != nil {
			return aimcp.ActionReceipt{}, err
		}
		if len(active) != 0 {
			return aimcp.ActionReceipt{}, errors.New("manual stat allocation is unavailable while a game task is active")
		}
	}
	switch a.Kind {
	case "move", "look", "talk", "window", "battle", "battle-end", "party", "duel", "chat", "mail", "item", "pet", "status", "allocate-stat", "social-setting", "trade":
	default:
		return aimcp.ActionReceipt{}, aimcp.ErrInvalidParams
	}
	if a.ExpectedRevision == 0 {
		return aimcp.ActionReceipt{}, aimcp.ErrInvalidParams
	}
	// MCP text is decoded as UTF-8. Keep it in TextUTF8 so aigame performs
	// the single, checked CP936 conversion at the legacy wire boundary.
	action := aigame.Action{Kind: aigame.ActionKind(a.Kind), X: a.X, Y: a.Y, Direction: a.Direction, Route: a.Route, TargetID: a.TargetID, Index: a.Index, Value: a.Value, Value2: a.Value2, WindowType: a.WindowType, WindowButton: a.WindowButton, WindowSequence: a.WindowSequence, WindowObjectID: a.WindowObjectID, WindowSelect: a.WindowSelect, Command: a.Command, TextUTF8: a.Text, Color: a.Color, Range: a.Range, PartyRequest: a.PartyRequest, PetSlot: a.PetSlot}
	if a.Kind == "battle" {
		command, err := battleActionCommand(a)
		if err != nil {
			return aimcp.ActionReceipt{}, err
		}
		action.Command = command
	}
	var receipt aimcp.ActionReceipt
	err := dispatchGameAction(ctx, b, func(sendCtx context.Context) error {
		s, err := b.Session.Observe(sendCtx)
		if err != nil {
			return err
		}
		if s.Account != binding.AccountID || s.Character != binding.CharacterName {
			return aimcp.ErrInvalidBinding
		}
		if s.Revision != a.ExpectedRevision {
			return fmt.Errorf("%w: game observation revision changed", aigame.ErrStaleRevision)
		}
		// Agent action callers have always required a configured policy. A
		// leveling owner only applies that same policy fence when the server has
		// explicitly configured one; an ordinary leveling run with no build keeps
		// its existing behavior and simply performs no automatic allocation.
		if a.Kind == "allocate-stat" && (b.Owner == aicontrol.Agent || (b.Owner == aicontrol.Leveling && b.CharacterBuild != nil)) {
			index, allowed := b.nextBuildAttribute(s)
			if !allowed || int32(index) != a.Index {
				return fmt.Errorf("%w: allocation does not match the configured character build and current state", aimcp.ErrInvalidParams)
			}
		}
		// Record uncertainty before the write. A crash between the write and
		// result persistence must never cause an automatic replay.
		var created bool
		receipt, created, err = b.Receipts.PrepareOnce(sendCtx, binding, a)
		if err != nil {
			return err
		}
		if !created {
			return nil
		}
		if isMailListAction(a) {
			receipt.Evidence, err = b.prepareMailListEvidence(binding, s)
			if err != nil {
				return err
			}
			// Persist the baseline before writing AB. A crash after the write
			// must retain enough context to reject an old/cached table.
			if err := b.Receipts.Save(sendCtx, binding, receipt); err != nil {
				return err
			}
		}
		if a.Kind == "allocate-stat" {
			if err := b.prepareStatAllocation(sendCtx, s, a, &receipt); err != nil {
				return err
			}
		}
		writeCtx, cancel := context.WithTimeout(sendCtx, 3*time.Second)
		defer cancel()
		err = b.Session.ExecuteExpected(writeCtx, a.ExpectedRevision, action)
		if err != nil {
			if errors.Is(err, aigame.ErrStaleRevision) || errors.Is(err, aigame.ErrInvalidAction) || errors.Is(err, aigame.ErrWrongPhase) || errors.Is(err, aigame.ErrBattleNotReady) {
				if a.Kind == "allocate-stat" {
					delete(b.statAllocations, receipt.Handle)
				}
				receipt.Status = aimcp.ReceiptFailed
				receipt.Reason = "action rejected before submission; observe current state before choosing another action"
				persist, stop := context.WithTimeout(context.WithoutCancel(sendCtx), 2*time.Second)
				defer stop()
				return b.Receipts.Save(persist, binding, receipt)
			}
			return nil
		} // durable unknown receipt is the honest result
		receipt.Reason = "packet submitted; game outcome requires authoritative observation"
		// No generic packet acknowledgement establishes quest, purchase,
		// party, PK or level success. Deterministic tasks confirm predicates.
		return b.Receipts.Save(sendCtx, binding, receipt)
	})
	return receipt, err
}

var _ aimcp.Backend = (*GameBackend)(nil)
