// Package aigame provides a headless StoneAge 2.5 game session.
//
// A Session is a real named-LSSPROTO client connection.  It talks to the
// configured compatibility gateway and therefore goes through the same
// ClientLogin authentication path as the web client.  It does not emulate
// encounters or write game state directly.
package aigame

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"time"
)

// Phase is the state of the server-side client state machine.
type Phase string

const (
	PhaseDisconnected  Phase = "disconnected"
	PhaseGreeting      Phase = "greeting"
	PhaseAuthenticated Phase = "authenticated"
	PhaseCharacterList Phase = "character-list"
	PhaseWorld         Phase = "world"
	PhaseBattle        Phase = "battle"
)

// ActionKind identifies a legal client operation.
type ActionKind string

const (
	ActionMove ActionKind = "move"
	ActionLook ActionKind = "look"
	// ActionMapEvent submits the native EV request used by the legacy client
	// after it has reached a map event tile. The server owns the result; the
	// action only carries the event identity, client correlation sequence,
	// reached coordinates, and facing direction.
	ActionMapEvent  ActionKind = "map-event"
	ActionTalk      ActionKind = "talk"
	ActionWindow    ActionKind = "window"
	ActionBattle    ActionKind = "battle"
	ActionBattleEnd ActionKind = "battle-end"
	ActionParty     ActionKind = "party"
	ActionDuel      ActionKind = "duel"
	ActionChat      ActionKind = "chat"
	// ActionMail covers the native address-book mail path.  Its Command is
	// "list" (AB), "add" (AAB at the current position), or "send"
	// (MSG to an observed address-book slot).
	ActionMail          ActionKind = "mail"
	ActionItem          ActionKind = "item"
	ActionPet           ActionKind = "pet"
	ActionStatus        ActionKind = "status"
	ActionAllocateStat  ActionKind = "allocate-stat"
	ActionSocialSetting ActionKind = "social-setting"
	ActionTrade         ActionKind = "trade"
	ActionRaw           ActionKind = "raw"
)

// Native 2.5 CHAR_EVENT values accepted by MapEvent. These values are from
// server/legacy/source/2.5/gmsv/include/char_base.h; keeping them here avoids
// allowing an arbitrary event number to reach EVENT_main.
const (
	MapEventEnemy       int32 = 2
	MapEventWarp        int32 = 3
	MapEventWarpMorning int32 = 6
	MapEventWarpNoon    int32 = 7
	MapEventWarpNight   int32 = 8
)

var nextMapEventSequence uint32

// NextMapEventSequence returns a positive process-wide correlation sequence
// for a native EV request. Movement and leveling use the same allocator so
// concurrent executors cannot reuse a sequence and consume one another's EV
// acknowledgement.
func NextMapEventSequence() int32 {
	sequence := int32(atomic.AddUint32(&nextMapEventSequence, 1) & 0x7fffffff)
	if sequence != 0 {
		return sequence
	}
	sequence = int32(atomic.AddUint32(&nextMapEventSequence, 1) & 0x7fffffff)
	if sequence == 0 {
		return 1
	}
	return sequence
}

var (
	ErrClosed            = errors.New("aigame session is closed")
	ErrNotAuthenticated  = errors.New("aigame session is not authenticated")
	ErrCharacterRequired = errors.New("aigame character is not logged in")
	ErrInvalidAction     = errors.New("invalid aigame action")
	ErrWrongPhase        = errors.New("action is not valid in the current game phase")
	ErrBattleNotReady    = errors.New("battle command is not ready for the current server turn")
	ErrStaleRevision     = errors.New("aigame snapshot revision is stale")
	ErrTextEncoding      = errors.New("aigame text cannot be represented in CP936")
	ErrUnexpectedReply   = errors.New("unexpected StoneAge server reply")
	ErrProtocol          = errors.New("invalid StoneAge protocol packet")
)

// Credentials are deliberately byte-oriented.  The preserved client uses
// the Windows code page for legacy text; callers which already have CP936
// bytes can use the byte fields without a lossy UTF-8 conversion.
type Credentials struct {
	Account       string
	Password      string
	AccountBytes  []byte
	PasswordBytes []byte
}

func (credentials Credentials) accountBytes() []byte {
	if credentials.AccountBytes != nil {
		return append([]byte(nil), credentials.AccountBytes...)
	}
	return []byte(credentials.Account)
}

func (credentials Credentials) passwordBytes() []byte {
	if credentials.PasswordBytes != nil {
		return append([]byte(nil), credentials.PasswordBytes...)
	}
	return []byte(credentials.Password)
}

// Config controls one headless TCP session.  Address must point at the
// named-protocol gateway; it is intentionally not a raw numeric GMSV
// address.  Dial is primarily useful for deterministic protocol tests.
type Config struct {
	Address     string
	DialTimeout time.Duration
	PacketLimit int
	EventBuffer int
	Dial        func(context.Context, string) (net.Conn, error)
}

// DefaultConfig returns safe local development defaults.  Production callers
// should set Address from the configured game-server directory.
func DefaultConfig() Config {
	return Config{
		DialTimeout: 5 * time.Second,
		PacketLimit: 4 * 1024 * 1024,
		EventBuffer: 256,
	}
}

// Character is an account character returned by CharList.  Option is the
// opaque server option payload; it is retained so a manager can display or
// persist the original character-list metadata without guessing its layout.
type Character struct {
	Slot   int
	Name   string
	Option string
}

// AddressBookEntry is the server-owned recipient directory returned by the
// native AB/ABI packets. Index is a reusable address-book slot, so it is only
// valid together with AddressBookKnown and the current entry contents.
// Persistent character identity is intentionally absent: 2.5 AB packets carry
// the display name and status, but no durable character ID.
type AddressBookEntry struct {
	Index          int32
	Use            bool
	Online         bool
	Level          int32
	DuelPoint      int32
	Graphic        int32
	Name           string
	Transmigration int32
}

// CharacterCreate contains the thirteen legacy CreateNewChar fields.  Name
// and all text remain byte-oriented at the wire boundary; the string form is
// convenient for the normal ASCII character names used by the server.
type CharacterCreate struct {
	DataPlace int32
	Name      string
	NameBytes []byte
	Image     int32
	FaceImage int32
	Vital     int32
	Strength  int32
	Toughness int32
	Dexterity int32
	Earth     int32
	Water     int32
	Fire      int32
	Wind      int32
	Hometown  int32
}

func (create CharacterCreate) nameBytes() []byte {
	if create.NameBytes != nil {
		return append([]byte(nil), create.NameBytes...)
	}
	encoded, err := encodeLegacyUTF8(create.Name)
	if err != nil {
		return nil
	}
	return encoded
}

// Point is a server grid coordinate.  Floor is optional for action targets;
// the session's current floor is authoritative.
type Point struct {
	Floor     int32
	X         int32
	Y         int32
	Direction int32
}

// PlayerSnapshot contains the decoded P1/masked status projection.  Values
// not supplied by a particular server build remain zero; HasStatus indicates
// whether at least one authoritative status packet has been received.
type PlayerSnapshot struct {
	ID        int32
	Name      string
	Title     string
	HP        int32
	MaxHP     int32
	MP        int32
	MaxMP     int32
	Vital     int32
	Strength  int32
	Toughness int32
	Dexterity int32
	// UnspentStatPoints is authoritative after SKUP or the optional S:AI field.
	UnspentStatPoints int32
	StatPointsKnown   bool
	SocialFlags       int32
	SocialFlagsKnown  bool
	EXP               int32
	MaxEXP            int32
	Level             int32
	Attack            int32
	Defense           int32
	Quick             int32
	Charm             int32
	Luck              int32
	Earth             int32
	Water             int32
	Fire              int32
	Wind              int32
	Gold              int32
	TitleNo           int32
	DP                int32
	Transmigration    int32
	RidePet           int32
	RidePetKnown      bool
	BaseImage         int32
	HasStatus         bool
	// KS(slot,1) confirms the selected combat pet; zero is a real slot.
	BattlePetSlot      int32
	BattlePetSlotKnown bool
}

// SkillSnapshot is one server-owned character skill. Skill IDs and levels
// remain numeric because the legacy S status stream does not include display
// names; callers can resolve names from the versioned game catalog.
type SkillSnapshot struct {
	ID    int32
	Level int32
}

// PetSkillSnapshot is one server-owned pet skill slot. The W status stream
// includes field/target metadata and escaped display strings, so retain the
// values for deterministic battle and task adapters.
type PetSkillSnapshot struct {
	Index      int32
	ID         int32
	Field      int32
	Target     int32
	DeadTarget bool
	Name       string
	Memo       string
}

const AIObservationEventGroups = 6

// AIInventoryItem identifies an occupied backpack slot and its server item
// template. TemplateID is not a unique instance identity or a display graphic.
type AIInventoryItem struct {
	Slot       int32
	TemplateID int32
}

// AIObservation is the character-scoped response produced by the optional
// S("AI") server extension. Event values are persisted end/now bit fields.
// LearnRide is the authoritative riding permission level. Optional savepoint
// and inventory fields carry separate Known markers so an older response
// cannot imply a zero mask or an empty backpack.
type AIObservation struct {
	// PersistentCharacterID is present only after native persisted-load validation.
	PersistentCharacterID string
	PartyMode             int32
	PartyModeKnown        bool
	StatPoints            int32
	StatPointsKnown       bool
	RequestID             string
	Version               int32
	CharacterIndex        int32
	Pets                  []PetSnapshot
	EndEvents             [AIObservationEventGroups]int32
	NowEvents             [AIObservationEventGroups]int32
	LearnRide             int32
	SavePoints            int32
	SavePointsKnown       bool
	Items                 []AIInventoryItem
	ItemsKnown            bool
	Received              bool
}

// PetSnapshot identifies an owned pet as far as the current protocol allows.
// The 2.5 K status stream does not expose CHAR_CDKEY (or another globally
// unique pet ID), so a full K record is intentionally treated as unidentified
// until a server-side S("AI") response confirms the stable ID. Identity is a
// session-local continuity key: it survives masked K updates, but changes
// after a slot is emptied/reused or a full K replacement is observed. A
// visible PME actor may additionally have a transient server object ID.
type PetSnapshot struct {
	StableID      string
	Identity      string
	IdentityKnown bool
	IdentityEpoch uint64
	ID            int32
	Slot          int32
	Name          string
	FreeName      string
	Graphic       int32
	HP            int32
	MaxHP         int32
	MP            int32
	MaxMP         int32
	EXP           int32
	MaxEXP        int32
	Level         int32
	Attack        int32
	Defense       int32
	Quick         int32
	UseFlag       int32
	Alive         bool
	Skills        []PetSkillSnapshot
}

// InventoryItem is one of the twenty absolute legacy inventory slots.
type InventoryItem struct {
	Index      int32
	Name       string
	Name2      string
	Memo       string
	Graphic    int32
	Color      int32
	Field      int32
	Target     int32
	DeadTarget bool
	Level      int32
	Send       int32
}

// WindowSnapshot is the latest server-owned WN window.  Data is opaque by
// design: every NPC and shop uses the same envelope with different payload
// records, and the caller can select it using the documented window type.
type WindowSnapshot struct {
	Type       int32
	ButtonType int32
	Sequence   int32
	ObjectID   int32
	Data       string
	Open       bool
	// Submitted records a completed local WN write for this exact received
	// window. It is not a server acknowledgement or a confirmed game result.
	Submitted bool
}

// ActorSnapshot is a visible field object decoded from C/CA/CD/PME. Direction
// keeps the native CHAR_DIR value carried by those wire events; a renderer
// that uses a different sprite orientation must convert it at its own edge.
// ID is a reusable server object handle used for current actions; only the
// separately confirmed PersistentCharacterID identifies a person across sessions.
// Coordinates and names are observations, not client-authoritative state.
type ActorSnapshot struct {
	PersistentCharacterID string
	ID                    int32
	Kind                  string
	CharType              int32
	X                     int32
	Y                     int32
	Direction             int32
	Graphic               int32
	Level                 int32
	Name                  string
	FreeName              string
	Title                 string
	PetName               string
	PetLevel              int32
	Money                 int32
	ItemName              string
	Action                int32
}

// PartyMember is an N status record. Slot is a reusable party display slot,
// not a persistent person identity.
type PartyMember struct {
	PersistentCharacterID string
	Slot                  int32
	ID                    int32
	Name                  string
	Level                 int32
	HP                    int32
	MaxHP                 int32
	MP                    int32
}

// BattleParticipant is one BC roster record.  BattleID is the wire battle
// target used in H/T/J/I/W commands; it is not a field object ID.
type BattleParticipant struct {
	BattleID int32
	Name     string
	Title    string
	Graphic  int32
	Level    int32
	HP       int32
	MaxHP    int32
	Flags    int32
	RideFlag int32
	PetName  string
	PetLevel int32
	PetHP    int32
	PetMaxHP int32
	Player   bool
	Dead     bool
}

// BattleSnapshot mirrors the control packets used by BattleProc.  A command
// is legal only after BP and BC for the current turn have been observed and
// while CommandReady is true.  BA is retained separately because it is the
// animation/turn marker, not the menu acknowledgement.
type BattleSnapshot struct {
	Active          bool
	Type            int32
	Field           int32
	FieldAttack     int32
	MyNo            int32
	MyNoKnown       bool
	BPFlags         int32
	MyMP            int32
	Turn            int32
	AnimationFlags  int32
	BPReceived      bool
	BCReceived      bool
	BAReceived      bool
	CommandReady    bool
	PlayerSubmitted bool
	PetSubmitted    bool
	Movie           bool
	Ended           bool
	LastCommand     string
	Result          string
	Participants    []BattleParticipant
}

// ChatMessage is an observed TK/MSG/PMSG line.  Text keeps the bytes carried
// by the named protocol; for normal ASCII it is directly usable as a string.
type ChatMessage struct {
	// SpeakerCharacterID is attributed by the server at chat time.
	SpeakerCharacterID string
	// ContextID identifies the observation session, not the speaker. FromID
	// is a transient protocol value and cannot establish durable identity.
	ContextID string
	At        time.Time
	Channel   string
	FromID    int32
	Color     int32
	Text      string
}

// Snapshot is an immutable-at-return projection of the latest server state.
// Callers can safely retain it; slices and maps are deep-copied by
// Session.Snapshot.
type Snapshot struct {
	Revision         uint64
	At               time.Time
	Phase            Phase
	Connected        bool
	Account          string
	Character        string
	Characters       []Character
	Position         Point
	Player           PlayerSnapshot
	Skills           []SkillSnapshot
	Pets             []PetSnapshot
	Inventory        []InventoryItem
	Windows          []WindowSnapshot
	ActiveWindow     *WindowSnapshot
	Actors           []ActorSnapshot
	Party            []PartyMember
	AddressBookKnown bool
	AddressBook      []AddressBookEntry
	Chat             []ChatMessage
	Battle           BattleSnapshot
	// AddressBookRevision advances only when a complete AB table has been
	// parsed. ABI incremental packets never advance it. It is scoped to this
	// connection and reset when the character lifecycle clears the address
	// book, so a caller can require a fresh full response for mail:list.
	AddressBookRevision uint64
	// SessionToken identifies this connection's current character incarnation
	// for reconciliation only. It rotates on character lifecycle transitions,
	// is never projected to MCP, and is not a durable game identity.
	SessionToken string `json:"-"`
	Trade        TradeSnapshot
	// AI is the raw latest validated S("AI") own-state response. Owned pet
	// identities and mutable K status are reconciled into Pets; consumers
	// should use Pets for the current authoritative projection.
	AI AIObservation
	// AIObservationRevision is the Snapshot revision at which the latest
	// valid S("AI") response was applied. Zero means none arrived.
	AIObservationRevision uint64
	LastFunction          string
	LastError             string
}

// FieldKind describes one decoded server argument.
type FieldKind uint8

const (
	FieldRaw FieldKind = iota
	FieldInt
	FieldString
)

// Field contains both the wire token and its typed value.  Raw is retained
// for opaque payloads and diagnostics; Text is copied and never aliases the
// network read buffer.
type Field struct {
	Kind FieldKind
	Raw  string
	Int  int32
	Text []byte
}

func (field Field) String() string {
	if field.Kind == FieldString {
		return string(field.Text)
	}
	return field.Raw
}

// IntValue returns the decoded integer, or fallback when the field is not an
// integer.  It is useful when handling optional packets from older builds.
func (field Field) IntValue(fallback int32) int32 {
	if field.Kind == FieldInt {
		return field.Int
	}
	return fallback
}

// Event is one ordered server packet.  The raw encoded packet is never kept,
// which prevents accidental password/session material from entering event
// logs; only server function fields are exposed.
type Event struct {
	ID       uint32
	Function string
	Fields   []Field
	At       time.Time
	Sequence uint64
}

// Action is a validated operation to be encoded as one named-protocol
// request.  Use the constructor helpers where possible.  Fields is only for
// the explicitly supported RawAction and is still checked against the known
// schema; arbitrary wire injection is not permitted.
type Action struct {
	Kind ActionKind

	X, Y, Direction int32
	// Event and EventSequence are used only by ActionMapEvent. EventSequence
	// is echoed by the server's EV acknowledgement and is never interpreted as
	// a game-state revision.
	Event          int32
	EventSequence  int32
	Route          string
	TargetID       int32
	Index          int32
	Value          int32
	Value2         int32
	WindowType     int32
	WindowButton   int32
	WindowSequence int32
	WindowObjectID int32
	WindowSelect   int32
	Command        string
	Text           string
	TextBytes      []byte
	// TextUTF8 is public UTF-8 text for legacy fields. TextBytes remains the
	// explicit raw CP936 escape hatch for callers that already own wire bytes.
	TextUTF8     string
	Color        int32
	Range        int32
	PartyRequest int32
	PetSlot      int32
	Function     string
	Fields       []ActionField
}

// ActionField is used only by RawAction for an existing named function.
type ActionField struct {
	Int  *int32
	Text []byte
}

// Constructors make the wire intent explicit and are used by the higher
// level task/leveling engines.
func Move(x, y int32, route string) Action {
	return Action{Kind: ActionMove, X: x, Y: y, Route: route}
}

// Look sends the native one-direction LOOK request. The server uses the
// current authoritative player tile together with this facing direction to
// dispatch LOOKEDFUNC NPC interactions.
func Look(direction int32) Action {
	return Action{Kind: ActionLook, Direction: direction}
}

// MapEvent sends the native EV request. Coordinates must be the currently
// observed server position; a movement executor should call this only after
// confirming that it reached the verified event source tile. Direction -1 is
// the legacy sentinel used for warp events, while enemy events may provide a
// facing direction in the range 0..7.
func MapEvent(event, sequence, x, y, direction int32) Action {
	return Action{Kind: ActionMapEvent, Event: event, EventSequence: sequence,
		X: x, Y: y, Direction: direction}
}

func Talk(x, y int32, command string, color, talkRange int32) Action {
	return Action{Kind: ActionTalk, X: x, Y: y, Command: command, Color: color, Range: talkRange}
}

func Window(x, y, sequence, objectID, button int32, data string) Action {
	return Action{Kind: ActionWindow, X: x, Y: y, WindowSequence: sequence, WindowObjectID: objectID, WindowSelect: button, Text: data}
}

func Battle(command string) Action { return Action{Kind: ActionBattle, Command: command} }

func EndBattle() Action { return Action{Kind: ActionBattleEnd} }

func Party(x, y, request int32) Action {
	return Action{Kind: ActionParty, X: x, Y: y, PartyRequest: request}
}

func Duel(x, y int32) Action { return Action{Kind: ActionDuel, X: x, Y: y} }

func Chat(text string, color, talkRange int32) Action {
	return Action{Kind: ActionChat, Text: text, Color: color, Range: talkRange}
}

// Mailbox asks the server for the current address-book slots. The response
// arrives asynchronously as AB and is reflected by the next Snapshot.
func Mailbox() Action { return Action{Kind: ActionMail, Command: "list"} }

// AddMailContact sends the native AAB request for the player at the
// authoritative current tile. The server remains responsible for deciding
// whether the contact can be added.
func AddMailContact(x, y int32) Action {
	return Action{Kind: ActionMail, Command: "add", X: x, Y: y}
}

// Mail sends ordinary game mail through an already observed address-book
// slot. The slot must still be present when the action is submitted.
func Mail(index int32, text string, color int32) Action {
	return Action{Kind: ActionMail, Command: "send", Index: index, Text: text, Color: color}
}

func UseItem(x, y, index, target int32) Action {
	return Action{Kind: ActionItem, X: x, Y: y, Index: index, TargetID: target}
}

func ItemMove(from, to int32) Action {
	return Action{Kind: ActionItem, Index: from, Value: to, Command: "move"}
}

func SetPetStatus(slot, value int32) Action {
	return Action{Kind: ActionPet, PetSlot: slot, Value: value, Command: "status"}
}

func RawAction(function string, fields ...ActionField) Action {
	return Action{Kind: ActionRaw, Function: function, Fields: append([]ActionField(nil), fields...)}
}
