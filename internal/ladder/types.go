// Package ladder defines the player-facing ladder protocol. GMSV owns the
// rules and state; Web and sactl consume the same projection and receipts.
package ladder

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/text/encoding/simplifiedchinese"
)

const Version = 1
const Prefix = "LADDER|"

// HistoricalReceipt confirms an operation from a previous server lifetime.
// Its snapshot is current, but the old operation must not renew local control.
func (e Envelope) HistoricalReceipt() bool {
	return e.Replay && e.ServerBoot != "" && e.ReceiptBoot != "" && e.ServerBoot != e.ReceiptBoot
}

func NewRequestID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

// Clone keeps the mutable client projection separate from retained snapshots.
func (e Envelope) Clone() Envelope {
	e.Contacts = append([]Contact(nil), e.Contacts...)
	s := &e.Snapshot
	s.Invitations = append([]Invitation(nil), s.Invitations...)
	if s.Room != nil {
		r := *s.Room
		r.Members = append([]Player(nil), r.Members...)
		s.Room = &r
	}
	if s.Match != nil {
		m := *s.Match
		m.Teams = append([]Team(nil), m.Teams...)
		for i := range m.Teams {
			m.Teams[i].Members = append([]Player(nil), m.Teams[i].Members...)
		}
		s.Match = &m
	}
	if s.Result != nil {
		r := *s.Result
		r.Members = append([]ResultPlayer(nil), r.Members...)
		s.Result = &r
	}
	return e
}

type Player struct {
	ID                   string  `json:"id"`
	Strategy             string  `json:"strategy"`
	Name                 string  `json:"name"`
	NameHex              string  `json:"name_hex,omitempty"`
	Online               bool    `json:"online"`
	Ready                bool    `json:"ready"`
	Power                float64 `json:"power"`
	Rating               int     `json:"rating"`
	PetMask              int     `json:"pet_mask"`
	AvailablePetMask     int     `json:"available_pet_mask"`
	ActivePet            int     `json:"active_pet"`
	RidePet              int     `json:"ride_pet"`
	Idle                 bool    `json:"idle"`
	BusyReason           string  `json:"busy_reason,omitempty"`
	ReconnectRemainingMS int64   `json:"reconnect_remaining_ms"`
	Abandoned            bool    `json:"abandoned"`
}

type Room struct {
	ID         string   `json:"id"`
	LeaderID   string   `json:"leader_id"`
	Mode       int      `json:"mode"`
	Phase      string   `json:"phase"`
	QueuedAtMS int64    `json:"queued_at_ms"`
	Members    []Player `json:"members"`
}

type Invitation struct {
	ID          string `json:"id"`
	RoomID      string `json:"room_id"`
	From        Player `json:"from"`
	Mode        int    `json:"mode"`
	ExpiresAtMS int64  `json:"expires_at_ms"`
}

type Team struct {
	Members []Player `json:"members"`
	Power   float64  `json:"power"`
	Rating  float64  `json:"rating"`
}

type Match struct {
	ID          string  `json:"id"`
	Mode        int     `json:"mode"`
	Phase       string  `json:"phase"`
	StartAtMS   int64   `json:"start_at_ms"`
	StartedAtMS int64   `json:"started_at_ms"`
	Side        int     `json:"side"`
	PowerGap    float64 `json:"power_gap"`
	Teams       []Team  `json:"teams"`
}

type Statistics struct {
	PlayerKills     int   `json:"player_kills"`
	PetKills        int   `json:"pet_kills"`
	Deaths          int   `json:"deaths"`
	Assists         int   `json:"assists"`
	Damage          int64 `json:"damage"`
	DamageTaken     int64 `json:"damage_taken"`
	PetDamage       int64 `json:"pet_damage"`
	PetDamageTaken  int64 `json:"pet_damage_taken"`
	RideDamageTaken int64 `json:"ride_damage_taken"`
	Healing         int64 `json:"healing"`
	Revives         int   `json:"revives"`
}

type ResultPlayer struct {
	Player
	Side         int        `json:"side"`
	RatingBefore int        `json:"rating_before"`
	RatingDelta  int        `json:"rating_delta"`
	RatingAfter  int        `json:"rating_after"`
	Statistics   Statistics `json:"statistics"`
}

type Result struct {
	ID                   string         `json:"id"`
	Mode                 int            `json:"mode"`
	WinnerSide           int            `json:"winner_side"` // -1 is a draw or an unrated abort.
	Rated                bool           `json:"rated"`
	Reason               string         `json:"reason"`
	Turns                int            `json:"turns"`
	DurationMS           int64          `json:"duration_ms"`
	StatisticsIncomplete bool           `json:"statistics_incomplete,omitempty"`
	PowerVersion         string         `json:"power_version"`
	RatingVersion        string         `json:"rating_version"`
	Members              []ResultPlayer `json:"members"`
}

type Snapshot struct {
	Self            Player       `json:"self"`
	Phase           string       `json:"phase"`
	Ratings         [5]int       `json:"ratings"`
	Room            *Room        `json:"room"`
	Invitations     []Invitation `json:"invitations"`
	Match           *Match       `json:"match"`
	Result          *Result      `json:"result"`
	CooldownUntilMS int64        `json:"cooldown_until_ms"`
}

// Contact binds a displayed card to the exact character selected by the
// caller. A slot alone is unsafe because removing a card can reuse it.
type Contact struct {
	Slot    int    `json:"slot"`
	ID      string `json:"id"`
	Name    string `json:"name"`
	NameHex string `json:"name_hex,omitempty"`
	Online  bool   `json:"online"`
}

type Envelope struct {
	Version         int       `json:"version"`
	RequestID       string    `json:"request_id"`
	RequestHex      string    `json:"request_hex,omitempty"`
	RequestWire     string    `json:"request_wire,omitempty"`
	OK              bool      `json:"ok"`
	Code            string    `json:"code"`
	Replay          bool      `json:"replay"`
	AppliedRevision uint64    `json:"applied_revision"`
	ServerBoot      string    `json:"server_boot,omitempty"`
	ReceiptBoot     string    `json:"receipt_boot,omitempty"`
	Revision        uint64    `json:"revision"`
	Sequence        uint64    `json:"sequence"`
	ServerTimeMS    int64     `json:"server_time_ms"`
	Event           string    `json:"event"`
	Snapshot        Snapshot  `json:"snapshot"`
	Contacts        []Contact `json:"contacts,omitempty"`
}

// Events is a recoverable subscription batch. Stream scopes Cursor to one
// authenticated character connection. A new connection or truncated journal
// returns Gap and a full Snapshot; callers must replace their projection.
// Revisions are global server values, so numerical gaps alone are not loss.
type Events struct {
	Stream   string     `json:"stream"`
	Cursor   uint64     `json:"cursor"`
	Gap      bool       `json:"gap"`
	TimedOut bool       `json:"timed_out"`
	Events   []Envelope `json:"events"`
	Snapshot *Envelope  `json:"snapshot"`
}

// ReservesCharacter excludes autonomous world tasks while preparing or
// participating in a match. Answer-only battle policies remain compatible.
func (s Snapshot) ReservesCharacter() bool {
	return s.Self.Ready || s.Phase == "queued" || s.Phase == "countdown" ||
		s.Phase == "battle" || s.Phase == "settling" || s.Phase == "result"
}

// AutoBattleEligible scopes a client's ladder-only loop to preparation and
// the live match. A finished match never keeps an automatic writer alive.
func (s Snapshot) AutoBattleEligible() bool {
	if s.Self.Abandoned {
		return false
	}
	return (s.Phase == "lobby" && s.Self.Ready) || s.Phase == "queued" ||
		s.Phase == "countdown" || s.Phase == "battle"
}

// Request uses only ASCII tokens so neither legacy escaping nor a display
// name can change its meaning. Arguments are validated by both client and
// authoritative server; revision=0 is reserved for read-only operations.
type Request struct {
	ID        string `json:"request_id"`
	Revision  uint64 `json:"revision"`
	Operation string `json:"operation"`
	Argument  string `json:"argument"`
}

func token(value string, max int) bool {
	if len(value) == 0 || len(value) > max {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func InviteArgument(slot int, identity string) (string, error) {
	if slot < 0 || slot >= 80 || !token(identity, 64) {
		return "", fmt.Errorf("invite requires a contact slot and its character ID from ladder contacts")
	}
	return strconv.Itoa(slot) + ":" + identity, nil
}

func (r Request) Wire() (string, error) {
	if !token(r.ID, 64) {
		return "", fmt.Errorf("invalid ladder request ID")
	}
	read := r.Operation == "status" || r.Operation == "result" || r.Operation == "contacts"
	if !read && r.Revision == 0 {
		return "", fmt.Errorf("ladder mutation requires a current revision")
	}
	switch r.Operation {
	case "strategy":
		if !token(r.Argument, 32) {
			return "", fmt.Errorf("invalid ladder strategy ID")
		}
	case "status", "contacts", "ready", "unready", "queue", "cancel", "leave", "ack":
		if r.Argument != "" {
			return "", fmt.Errorf("%s takes no argument", r.Operation)
		}
	case "invite":
		slot, identity, ok := strings.Cut(r.Argument, ":")
		n, err := strconv.Atoi(slot)
		canonical, idErr := InviteArgument(n, identity)
		if !ok || err != nil || idErr != nil || canonical != r.Argument {
			return "", fmt.Errorf("invite requires <slot>:<character_id> from ladder contacts")
		}
	case "create", "mode", "loadout":
		n, err := strconv.Atoi(r.Argument)
		if err != nil || strconv.Itoa(n) != r.Argument {
			return "", fmt.Errorf("invalid numeric ladder argument")
		}
		if (r.Operation == "create" || r.Operation == "mode") && (n < 1 || n > 5) ||
			r.Operation == "loadout" && (n < 0 || n > 31) {
			return "", fmt.Errorf("ladder argument out of range")
		}
	case "accept", "decline", "kick", "leader":
		if !token(r.Argument, 64) {
			return "", fmt.Errorf("invalid ladder identifier")
		}
	case "result":
		if r.Argument != "" && !token(r.Argument, 64) {
			return "", fmt.Errorf("invalid match ID")
		}
	default:
		return "", fmt.Errorf("unknown ladder operation %q", r.Operation)
	}
	return fmt.Sprintf("%s%d|%s|%d|%s|%s", Prefix, Version, r.ID, r.Revision, r.Operation, r.Argument), nil
}

func Decode(wire string) (Envelope, error) {
	var e Envelope
	if !strings.HasPrefix(wire, Prefix) {
		return e, fmt.Errorf("not a ladder packet")
	}
	if len(wire) > 128*1024 {
		return e, fmt.Errorf("ladder packet too large")
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(wire, Prefix)), &e); err != nil {
		return e, err
	}
	if e.Version != Version {
		return e, fmt.Errorf("unsupported ladder protocol version %d", e.Version)
	}
	if e.RequestHex != "" {
		wire, err := hex.DecodeString(e.RequestHex)
		if err != nil || len(wire) >= 256 {
			return Envelope{}, fmt.Errorf("invalid ladder receipt request")
		}
		e.RequestWire, e.RequestHex = string(wire), ""
	}
	players := []*Player{&e.Snapshot.Self}
	for i := range e.Contacts {
		contact := &e.Contacts[i]
		if contact.NameHex == "" {
			continue
		}
		b, err := hex.DecodeString(contact.NameHex)
		if err != nil {
			return Envelope{}, fmt.Errorf("invalid ladder contact name encoding: %w", err)
		}
		decoded, err := simplifiedchinese.GBK.NewDecoder().Bytes(b)
		if err != nil {
			return Envelope{}, err
		}
		contact.Name, contact.NameHex = string(decoded), ""
	}
	if e.Snapshot.Room != nil {
		for i := range e.Snapshot.Room.Members {
			players = append(players, &e.Snapshot.Room.Members[i])
		}
	}
	for i := range e.Snapshot.Invitations {
		players = append(players, &e.Snapshot.Invitations[i].From)
	}
	if e.Snapshot.Match != nil {
		for i := range e.Snapshot.Match.Teams {
			for j := range e.Snapshot.Match.Teams[i].Members {
				players = append(players, &e.Snapshot.Match.Teams[i].Members[j])
			}
		}
	}
	if e.Snapshot.Result != nil {
		for i := range e.Snapshot.Result.Members {
			players = append(players, &e.Snapshot.Result.Members[i].Player)
		}
	}
	for _, p := range players {
		if p.NameHex == "" {
			continue
		}
		b, err := hex.DecodeString(p.NameHex)
		if err != nil {
			return Envelope{}, fmt.Errorf("invalid ladder name encoding: %w", err)
		}
		decoded, err := simplifiedchinese.GBK.NewDecoder().Bytes(b)
		if err != nil {
			return Envelope{}, err
		}
		p.Name = string(decoded)
		p.NameHex = ""
	}
	return e, nil
}
