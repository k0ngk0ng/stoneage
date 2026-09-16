package aigame

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/k0ngk0ng/stoneage/server/go/namedproto"
)

const (
	defaultPacketLimit = 4 * 1024 * 1024
	defaultEventBuffer = 256
	defaultDialTimeout = 5 * time.Second
	// CHAR_walk_init rejects routes longer than 32 bytes on the legacy
	// server. Keep the client-side validation aligned so an overlong move is
	// rejected before anything is written to the socket.
	maxRouteLength = 32
	maxChatBytes   = 70
)

type wireKind uint8

const (
	wireInt wireKind = iota
	wireString
)

// These schemas are the function-name side of the existing bridge's
// server/go/bridge translator.  Keeping the table here makes a headless
// session independent of the Web page while preserving exactly the same
// named-protocol field order.
var clientSchemas = map[string][]wireKind{
	"W":             {wireInt, wireInt, wireString},
	"w":             {wireInt, wireInt, wireString},
	"EV":            {wireInt, wireInt, wireInt, wireInt, wireInt},
	"EN":            {wireInt, wireInt},
	"DU":            {wireInt, wireInt},
	"EO":            {wireInt},
	"BU":            {wireInt},
	"JB":            {wireInt, wireInt},
	"LB":            {wireInt, wireInt},
	"B":             {wireString},
	"SKD":           {wireInt, wireInt},
	"ID":            {wireInt, wireInt, wireInt, wireInt},
	"PI":            {wireInt, wireInt, wireInt},
	"DI":            {wireInt, wireInt, wireInt},
	"DG":            {wireInt, wireInt, wireInt},
	"DP":            {wireInt, wireInt, wireInt},
	"MI":            {wireInt, wireInt},
	"MSG":           {wireInt, wireString, wireInt},
	"PMSG":          {wireInt, wireInt, wireInt, wireString, wireInt},
	"AB":            {},
	"DAB":           {wireInt},
	"AAB":           {wireInt, wireInt},
	"L":             {wireInt},
	"TK":            {wireInt, wireInt, wireString, wireInt, wireInt},
	"M":             {wireInt, wireInt, wireInt, wireInt, wireInt},
	"C":             {wireInt},
	"S":             {wireString},
	"FS":            {wireInt},
	"HL":            {wireInt},
	"PR":            {wireInt, wireInt, wireInt},
	"KS":            {wireInt},
	"AC":            {wireInt, wireInt, wireInt},
	"MU":            {wireInt, wireInt, wireInt, wireInt},
	"PS":            {wireInt, wireInt, wireInt, wireString},
	"ST":            {wireInt},
	"DT":            {wireInt},
	"FT":            {wireString},
	"SKUP":          {wireInt},
	"KN":            {wireInt, wireString},
	"WN":            {wireInt, wireInt, wireInt, wireInt, wireInt, wireString},
	"SP":            {wireInt, wireInt, wireInt},
	"ClientLogin":   {wireString, wireString},
	"CreateNewChar": {wireInt, wireString, wireInt, wireInt, wireInt, wireInt, wireInt, wireInt, wireInt, wireInt, wireInt, wireInt, wireInt},
	"CharDelete":    {wireString},
	"CharLogin":     {wireString},
	"CharList":      {},
	"CharLogout":    {},
	"ProcGet":       {},
	"PlayerNumGet":  {},
	"Echo":          {wireString},
	"Shutdown":      {wireString, wireInt},
	"TD":            {wireString},
	"FM":            {wireString},
	"PETST":         {wireInt, wireInt},
	"MA":            {wireInt, wireInt, wireInt},
	"SPET":          {wireInt},
}

// Server schemas cover the packets emitted by the preserved 2.5 GMSV and
// translated by the existing named gateway.  Unknown function names are
// still surfaced as raw fields so an added server extension is observable;
// it is never guessed into a typed state field.
var serverSchemas = map[string][]wireKind{
	"XYD":           {wireInt, wireInt, wireInt},
	"EV":            {wireInt, wireInt},
	"EN":            {wireInt, wireInt},
	"RS":            {wireString},
	"RD":            {wireString},
	"B":             {wireString},
	"BC":            {wireString},
	"I":             {wireString},
	"SI":            {wireInt, wireInt},
	"MSG":           {wireInt, wireString, wireInt},
	"PMSG":          {wireInt, wireInt, wireInt, wireString, wireInt},
	"PME":           {wireInt, wireInt, wireInt, wireInt, wireInt, wireInt, wireInt, wireString},
	"AB":            {wireString},
	"ABI":           {wireInt, wireString},
	"TK":            {wireInt, wireString, wireInt},
	"MC":            {wireInt, wireInt, wireInt, wireInt, wireInt, wireInt, wireInt, wireInt, wireString},
	"M":             {wireInt, wireInt, wireInt, wireInt, wireInt, wireString},
	"C":             {wireString},
	"CA":            {wireString},
	"CD":            {wireString},
	"R":             {wireString},
	"S":             {wireString},
	"D":             {wireInt, wireInt, wireInt, wireString},
	"FS":            {wireInt},
	"HL":            {wireInt},
	"PR":            {wireInt, wireInt},
	"KS":            {wireInt, wireInt},
	"PS":            {wireInt, wireInt, wireInt, wireInt},
	"SKUP":          {wireInt},
	"WN":            {wireInt, wireInt, wireInt, wireInt, wireString},
	"EF":            {wireInt, wireInt, wireString},
	"SE":            {wireInt, wireInt, wireInt, wireInt},
	"ClientLogin":   {wireString},
	"CreateNewChar": {wireString, wireString},
	"CharDelete":    {wireString, wireString},
	"CharLogin":     {wireString, wireString},
	"CharList":      {wireString, wireString},
	"CharLogout":    {wireString, wireString},
	"ProcGet":       {wireString},
	"PlayerNumGet":  {wireInt, wireInt},
	"Echo":          {wireString},
	"NU":            {wireInt},
	"TD":            {wireString},
	"FM":            {wireString},
	"WO":            {wireInt},
	"IC":            {wireInt, wireInt},
	"NC":            {wireInt},
	"PETST":         {wireInt, wireInt},
	"PETS":          {wireInt, wireInt},
	"SPET":          {wireInt, wireInt},
}

// Session owns one ordered TCP stream.  The network reader is started after
// CharLogin succeeds; login/character-select requests are synchronously
// consumed under requestMu so no two readers ever race on a legacy socket.
type Session struct {
	conn   net.Conn
	reader *bufio.Reader
	cfg    Config

	writeMu   sync.Mutex
	requestMu sync.Mutex
	stateMu   sync.RWMutex

	state  gameState
	nextID uint32

	// eventMu protects the lifecycle of the public event stream. A publisher
	// registers before it can send, so finish can stop new publishers and wait
	// for blocked sends to observe done before closing events.
	eventMu      sync.Mutex
	eventCond    *sync.Cond
	eventClosing bool
	eventSenders int
	events       chan Event
	// mapEventAcks is a private side channel for deterministic callers which
	// submit EV. The public Events stream is consumed by the provider wake
	// loop, so an EV waiter must not steal that stream's packet.
	mapEventAckMu sync.Mutex
	mapEventAcks  map[int32]Event
	mapEventOrder []int32
	mapEventWake  chan struct{}
	done          chan struct{}
	termMu        sync.RWMutex
	termErr       error
	closeOnce     sync.Once
	wg            sync.WaitGroup
	reading       bool
}

// NewSession wraps an already connected socket.  It does not read the
// greeting or authenticate; Connect is the normal entry point.  This
// constructor is useful for a gateway-owned connection and protocol tests.
func NewSession(connection net.Conn, config Config) *Session {
	config = normalizeConfig(config)
	buffer := config.EventBuffer
	if buffer <= 0 {
		buffer = defaultEventBuffer
	}
	session := &Session{
		conn:         connection,
		reader:       bufio.NewReaderSize(connection, 128*1024),
		cfg:          config,
		nextID:       1,
		events:       make(chan Event, buffer),
		mapEventAcks: make(map[int32]Event), mapEventWake: make(chan struct{}),
		done:  make(chan struct{}),
		state: newGameState(connection != nil),
	}
	session.eventCond = sync.NewCond(&session.eventMu)
	return session
}

func normalizeConfig(config Config) Config {
	if config.DialTimeout <= 0 {
		config.DialTimeout = defaultDialTimeout
	}
	if config.PacketLimit <= 0 {
		config.PacketLimit = defaultPacketLimit
	}
	if config.EventBuffer <= 0 {
		config.EventBuffer = defaultEventBuffer
	}
	return config
}

// Connect dials the named-protocol gateway, validates its LSSPROTO greeting,
// authenticates with ClientLogin and reads the account's character list.
// The password is used only to build the login packet and is never retained
// by Session or included in Event/Snapshot values.
func Connect(ctx context.Context, config Config, credentials Credentials) (*Session, error) {
	config = normalizeConfig(config)
	if strings.TrimSpace(config.Address) == "" {
		return nil, fmt.Errorf("%w: missing named gateway address", ErrProtocol)
	}
	dial := config.Dial
	if dial == nil {
		dialer := net.Dialer{Timeout: config.DialTimeout}
		dial = func(ctx context.Context, address string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp", address)
		}
	}
	connection, err := dial(ctx, config.Address)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("dial named gateway: %w", err)
	}
	session := NewSession(connection, config)
	if err := session.authenticate(ctx, credentials); err != nil {
		_ = session.Close()
		return nil, err
	}
	return session, nil
}

// Dial is an alias for Connect for callers that prefer net.Dial naming.
func Dial(ctx context.Context, config Config, credentials Credentials) (*Session, error) {
	return Connect(ctx, config, credentials)
}

// Login is the concise public name used by headless callers. It performs the
// named gateway handshake, ClientLogin and CharList request.
func Login(ctx context.Context, config Config, credentials Credentials) (*Session, error) {
	return Connect(ctx, config, credentials)
}

// Authenticate performs ClientLogin on a NewSession socket and leaves the
// session in character-select phase.  It is safe to call only once.
func (session *Session) Authenticate(ctx context.Context, credentials Credentials) error {
	if session == nil || session.conn == nil {
		return ErrClosed
	}
	return session.authenticate(ctx, credentials)
}

func (session *Session) authenticate(ctx context.Context, credentials Credentials) error {
	session.stateMu.RLock()
	phase := session.state.snapshot.Phase
	session.stateMu.RUnlock()
	if phase != PhaseGreeting {
		return fmt.Errorf("%w: authenticate requires greeting phase, got %s", ErrWrongPhase, phase)
	}
	if len(credentials.accountBytes()) == 0 || len(credentials.accountBytes()) > 15 {
		return fmt.Errorf("%w: account must contain 1..15 bytes", ErrProtocol)
	}
	if len(credentials.passwordBytes()) == 0 {
		return fmt.Errorf("%w: password is empty", ErrNotAuthenticated)
	}
	if err := session.readGreeting(ctx); err != nil {
		return err
	}
	account := string(credentials.accountBytes())
	response, err := session.request(ctx, "ClientLogin", []wireValue{
		{kind: wireString, text: credentials.accountBytes()},
		{kind: wireString, text: credentials.passwordBytes()},
	})
	if err != nil {
		return fmt.Errorf("ClientLogin: %w", err)
	}
	if responseText(response, 0) != "ok" {
		return fmt.Errorf("%w: server rejected ClientLogin", ErrNotAuthenticated)
	}
	session.stateMu.Lock()
	session.state.snapshot.Account = account
	session.state.snapshot.Phase = PhaseAuthenticated
	session.state.snapshot.Connected = true
	session.state.snapshot.At = time.Now()
	session.stateMu.Unlock()
	if _, err := session.request(ctx, "CharList", nil); err != nil {
		return fmt.Errorf("CharList: %w", err)
	}
	session.stateMu.Lock()
	session.state.snapshot.Phase = PhaseCharacterList
	session.stateMu.Unlock()
	return nil
}

func (session *Session) readGreeting(ctx context.Context) error {
	if session == nil || session.conn == nil {
		return ErrClosed
	}
	data, err := session.readN(ctx, 2)
	if err != nil {
		return fmt.Errorf("read named gateway greeting: %w", err)
	}
	if !bytes.Equal(data, []byte{'L', 0}) {
		return fmt.Errorf("%w: unexpected gateway greeting %x", ErrProtocol, data)
	}
	session.stateMu.Lock()
	session.state.snapshot.Phase = PhaseGreeting
	session.state.snapshot.Connected = true
	session.stateMu.Unlock()
	return nil
}

// Characters returns the latest CharList projection.  It does not issue a
// request; use RefreshCharacters when a new server packet is required.
func (session *Session) Characters() []Character {
	snapshot := session.Snapshot()
	return append([]Character(nil), snapshot.Characters...)
}

// RefreshCharacters asks the authenticated gateway for the current list.
func (session *Session) RefreshCharacters(ctx context.Context) ([]Character, error) {
	if err := session.requirePhase(PhaseAuthenticated, PhaseCharacterList); err != nil {
		return nil, err
	}
	if _, err := session.request(ctx, "CharList", nil); err != nil {
		return nil, fmt.Errorf("CharList: %w", err)
	}
	return session.Characters(), nil
}

// CreateCharacter sends the stock thirteen-field CreateNewChar request.  It
// never grants inventory or bypasses server-side creation checks.
func (session *Session) CreateCharacter(ctx context.Context, create CharacterCreate) error {
	if err := session.requirePhase(PhaseAuthenticated, PhaseCharacterList); err != nil {
		return err
	}
	nameBytes := create.NameBytes
	if nameBytes == nil {
		var err error
		nameBytes, err = encodeLegacyUTF8(create.Name)
		if err != nil {
			return fmt.Errorf("%w: character name: %v", ErrTextEncoding, err)
		}
	}
	values := []wireValue{
		{kind: wireInt, integer: create.DataPlace},
		{kind: wireString, text: append([]byte(nil), nameBytes...)},
		{kind: wireInt, integer: create.Image},
		{kind: wireInt, integer: create.FaceImage},
		{kind: wireInt, integer: create.Vital},
		{kind: wireInt, integer: create.Strength},
		{kind: wireInt, integer: create.Toughness},
		{kind: wireInt, integer: create.Dexterity},
		{kind: wireInt, integer: create.Earth},
		{kind: wireInt, integer: create.Water},
		{kind: wireInt, integer: create.Fire},
		{kind: wireInt, integer: create.Wind},
		{kind: wireInt, integer: create.Hometown},
	}
	response, err := session.request(ctx, "CreateNewChar", values)
	if err != nil {
		return fmt.Errorf("CreateNewChar: %w", err)
	}
	if responseText(response, 0) != "successful" {
		return fmt.Errorf("%w: server rejected character creation", ErrUnexpectedReply)
	}
	return nil
}

// EnterCharacter performs CharLogin and starts the asynchronous gameplay
// event reader after the successful response.  name must be one of the
// latest CharList entries; the server remains the final authority.
func (session *Session) EnterCharacter(ctx context.Context, name string) error {
	if err := session.requirePhase(PhaseAuthenticated, PhaseCharacterList); err != nil {
		return err
	}
	nameBytes, err := encodeLegacyUTF8(name)
	if err != nil {
		return fmt.Errorf("%w: character name: %v", ErrTextEncoding, err)
	}
	if len(nameBytes) == 0 {
		return fmt.Errorf("%w: empty character name", ErrInvalidAction)
	}
	response, err := session.request(ctx, "CharLogin", []wireValue{{kind: wireString, text: nameBytes}})
	if err != nil {
		return fmt.Errorf("CharLogin: %w", err)
	}
	if responseText(response, 0) != "successful" {
		return fmt.Errorf("%w: server rejected character login", ErrUnexpectedReply)
	}
	session.stateMu.Lock()
	session.state.snapshot.Character = name
	session.state.snapshot.Phase = PhaseWorld
	session.state.snapshot.Battle = BattleSnapshot{}
	session.state.snapshot.Connected = true
	session.stateMu.Unlock()
	session.startReader()
	return nil
}

// Enter is an alias for EnterCharacter for game adapters.
func (session *Session) Enter(ctx context.Context, name string) error {
	return session.EnterCharacter(ctx, name)
}

// Logout sends the native CharLogout request and waits for its result.  The
// socket is then closed; reopening a character uses a fresh authenticated
// session and avoids stale server-side state.
func (session *Session) Logout(ctx context.Context) error {
	if err := session.requirePhase(PhaseWorld, PhaseBattle); err != nil {
		return err
	}
	_, err := session.request(ctx, "CharLogout", nil)
	_ = session.Close()
	if err != nil {
		return fmt.Errorf("CharLogout: %w", err)
	}
	return nil
}

func (session *Session) request(ctx context.Context, function string, values []wireValue) (Event, error) {
	session.requestMu.Lock()
	defer session.requestMu.Unlock()
	if err := session.ensureOpen(); err != nil {
		return Event{}, err
	}
	packet, err := session.buildPacket(function, values)
	if err != nil {
		return Event{}, err
	}
	if err := session.writePacket(ctx, packet); err != nil {
		return Event{}, err
	}
	for {
		packet, err := session.readPacket(ctx)
		if err != nil {
			return Event{}, err
		}
		event, err := decodeEvent(packet)
		if err != nil {
			return Event{}, err
		}
		session.applyEvent(event)
		if event.Function == function {
			return event, nil
		}
	}
}

type wireValue struct {
	kind    wireKind
	integer int32
	text    []byte
}

func (session *Session) buildPacket(function string, values []wireValue) ([]byte, error) {
	schema, ok := clientSchemas[function]
	if !ok {
		return nil, fmt.Errorf("%w: unsupported client function %q", ErrInvalidAction, function)
	}
	if len(values) != len(schema) {
		return nil, fmt.Errorf("%w: %s expects %d fields, got %d", ErrInvalidAction, function, len(schema), len(values))
	}
	fields := make([]string, len(values))
	for index, value := range values {
		if value.kind != schema[index] {
			return nil, fmt.Errorf("%w: %s field %d has wrong type", ErrInvalidAction, function, index)
		}
		if value.kind == wireInt {
			fields[index] = namedproto.EncodeInt(value.integer)
		} else {
			fields[index] = namedproto.EncodeString(value.text)
		}
	}
	id := session.nextMessageID()
	raw, err := namedproto.RawMessage(id, function, fields)
	if err != nil {
		return nil, err
	}
	return namedproto.EncodePacket(raw)
}

func (session *Session) nextMessageID() uint32 {
	session.stateMu.Lock()
	defer session.stateMu.Unlock()
	id := session.nextID
	if id == 0 {
		id = 1
	}
	session.nextID = id + 1
	if session.nextID == 0 {
		session.nextID = 1
	}
	return id
}

func (session *Session) ensureOpen() error {
	if session == nil || session.conn == nil {
		return ErrClosed
	}
	select {
	case <-session.done:
		return ErrClosed
	default:
		return nil
	}
}

func (session *Session) requirePhase(phases ...Phase) error {
	if err := session.ensureOpen(); err != nil {
		return err
	}
	session.stateMu.RLock()
	phase := session.state.snapshot.Phase
	session.stateMu.RUnlock()
	for _, expected := range phases {
		if phase == expected {
			return nil
		}
	}
	return fmt.Errorf("%w: current phase is %s", ErrWrongPhase, phase)
}

func (session *Session) readN(ctx context.Context, count int) ([]byte, error) {
	if err := session.ensureOpen(); err != nil {
		return nil, err
	}
	var result []byte
	var err error
	stop := session.watchContext(ctx)
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = session.conn.SetReadDeadline(deadline)
		defer session.conn.SetReadDeadline(time.Time{})
	}
	result = make([]byte, count)
	_, err = io.ReadFull(session.conn, result)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	return result, nil
}

func (session *Session) readPacket(ctx context.Context) ([]byte, error) {
	if err := session.ensureOpen(); err != nil {
		return nil, err
	}
	stop := session.watchContext(ctx)
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = session.conn.SetReadDeadline(deadline)
		defer session.conn.SetReadDeadline(time.Time{})
	}
	packet, err := session.reader.ReadBytes('\n')
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	if len(packet) > session.cfg.PacketLimit {
		return nil, fmt.Errorf("%w: packet exceeds %d bytes", ErrProtocol, session.cfg.PacketLimit)
	}
	return packet, nil
}

func (session *Session) writePacket(ctx context.Context, packet []byte) error {
	if err := session.ensureOpen(); err != nil {
		return err
	}
	session.writeMu.Lock()
	defer session.writeMu.Unlock()
	if deadline, ok := ctx.Deadline(); ok {
		_ = session.conn.SetWriteDeadline(deadline)
		defer session.conn.SetWriteDeadline(time.Time{})
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for len(packet) > 0 {
		written, err := session.conn.Write(packet)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if written <= 0 {
			return io.ErrUnexpectedEOF
		}
		packet = packet[written:]
	}
	return nil
}

// watchContext lets a context cancel a blocking TCP read.  Cancellation of a
// handshake is terminal for that socket, as it cannot safely be reused after
// a partially consumed packet.  It does not log or expose the context value.
func (session *Session) watchContext(ctx context.Context) func() {
	if ctx == nil {
		return func() {}
	}
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = session.conn.Close()
		case <-stop:
		}
	}()
	return func() { close(stop) }
}

func (session *Session) startReader() {
	session.stateMu.Lock()
	if session.reading {
		session.stateMu.Unlock()
		return
	}
	session.reading = true
	session.stateMu.Unlock()
	session.wg.Add(1)
	go func() {
		defer session.wg.Done()
		for {
			packet, err := session.readPacketBackground()
			if err != nil {
				session.finish(err)
				return
			}
			event, err := decodeEvent(packet)
			if err != nil {
				session.finish(err)
				return
			}
			session.applyAndPublish(event)
		}
	}()
}

func (session *Session) readPacketBackground() ([]byte, error) {
	packet, err := session.reader.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	if len(packet) > session.cfg.PacketLimit {
		return nil, fmt.Errorf("%w: packet exceeds %d bytes", ErrProtocol, session.cfg.PacketLimit)
	}
	return packet, nil
}

func (session *Session) applyAndPublish(event Event) {
	session.applyEvent(event)
	session.stateMu.Lock()
	event.Sequence = session.state.snapshot.Revision
	session.stateMu.Unlock()
	if !session.beginEventPublish() {
		return
	}
	defer session.endEventPublish()
	select {
	case session.events <- cloneEvent(event):
	case <-session.done:
	}
}

// beginEventPublish reserves the event channel before selecting on the send.
// finish marks the stream as closing under the same lock, preventing a late
// publisher from entering after events has been closed.
func (session *Session) beginEventPublish() bool {
	session.eventMu.Lock()
	defer session.eventMu.Unlock()
	if session.eventClosing || session.events == nil {
		return false
	}
	session.eventSenders++
	return true
}

func (session *Session) endEventPublish() {
	session.eventMu.Lock()
	if session.eventSenders > 0 {
		session.eventSenders--
	}
	if session.eventClosing && session.eventSenders == 0 && session.eventCond != nil {
		session.eventCond.Broadcast()
	}
	session.eventMu.Unlock()
}

// Events returns the ordered server event stream.  It closes when the TCP
// session terminates.  Next is preferable when the caller needs cancellation.
func (session *Session) Events() <-chan Event { return session.events }

// Errors returns a terminal error only through Next; this method exists for
// callers which want to observe closure while separately draining Events.
func (session *Session) Done() <-chan struct{} { return session.done }

// Next waits for the next decoded server packet or context cancellation.
func (session *Session) Next(ctx context.Context) (Event, error) {
	if session == nil {
		return Event{}, ErrClosed
	}
	select {
	case event, ok := <-session.events:
		if !ok {
			session.termMu.RLock()
			err := session.termErr
			session.termMu.RUnlock()
			if err == nil {
				return Event{}, ErrClosed
			}
			return Event{}, err
		}
		return event, nil
	case <-ctx.Done():
		return Event{}, ctx.Err()
	case <-session.done:
		select {
		case event, ok := <-session.events:
			if ok {
				return event, nil
			}
		default:
		}
		session.termMu.RLock()
		err := session.termErr
		session.termMu.RUnlock()
		if err == nil {
			return Event{}, ErrClosed
		}
		return Event{}, err
	}
}

// Snapshot returns a deep copy of the latest state projection.
func (session *Session) Snapshot() Snapshot {
	if session == nil {
		return Snapshot{Phase: PhaseDisconnected}
	}
	session.stateMu.RLock()
	defer session.stateMu.RUnlock()
	return cloneSnapshot(session.snapshotLocked())
}

func (session *Session) snapshotLocked() Snapshot {
	snapshot := session.state.snapshot
	// Keep the private reconciliation metadata sourced from the connection
	// state even when a test adapter or lifecycle path updates the backing
	// fields before the next ordinary event.
	snapshot.SessionToken = session.state.sessionToken
	snapshot.AddressBookRevision = session.state.addressBookRevision
	snapshot.Trade = cloneTradeSnapshot(session.state.trade.snapshot)
	snapshot.Characters = append([]Character(nil), session.state.characters...)
	snapshot.AI.Pets = clonePetSnapshots(session.state.snapshot.AI.Pets)
	snapshot.Skills = append([]SkillSnapshot(nil), session.state.snapshot.Skills...)
	snapshot.Pets = snapshot.Pets[:0]
	for _, pet := range session.state.pets {
		snapshot.Pets = append(snapshot.Pets, pet)
	}
	sortPets(snapshot.Pets)
	snapshot.Pets = clonePetSnapshots(snapshot.Pets)
	snapshot.Inventory = snapshot.Inventory[:0]
	for _, item := range session.state.inventory {
		snapshot.Inventory = append(snapshot.Inventory, item)
	}
	sortInventory(snapshot.Inventory)
	snapshot.Actors = snapshot.Actors[:0]
	for _, actor := range session.state.actors {
		snapshot.Actors = append(snapshot.Actors, actor)
	}
	sortActors(snapshot.Actors)
	snapshot.Party = snapshot.Party[:0]
	for _, member := range session.state.party {
		snapshot.Party = append(snapshot.Party, member)
	}
	sortParty(snapshot.Party)
	snapshot.AddressBookKnown = session.state.addressBookKnown
	snapshot.AddressBook = snapshot.AddressBook[:0]
	for _, entry := range session.state.addressBook {
		snapshot.AddressBook = append(snapshot.AddressBook, entry)
	}
	sortAddressBook(snapshot.AddressBook)
	snapshot.Windows = append([]WindowSnapshot(nil), session.state.windows...)
	if session.state.activeWindow != nil {
		window := *session.state.activeWindow
		snapshot.ActiveWindow = &window
	} else {
		snapshot.ActiveWindow = nil
	}
	snapshot.Chat = append([]ChatMessage(nil), session.state.chat...)
	snapshot.Battle.Participants = append([]BattleParticipant(nil), session.state.snapshot.Battle.Participants...)
	return snapshot
}

func (session *Session) finish(err error) {
	session.closeOnce.Do(func() {
		if err == nil {
			err = ErrClosed
		}
		session.termMu.Lock()
		session.termErr = err
		session.termMu.Unlock()
		if session.conn != nil {
			_ = session.conn.Close()
		}
		session.stateMu.Lock()
		session.state.snapshot.Connected = false
		invalidateTradeForLifecycleLocked(&session.state)
		clearAddressBookLocked(&session.state)
		session.state.snapshot.Phase = PhaseDisconnected
		session.state.snapshot.At = time.Now()
		session.state.snapshot.Revision++
		session.stateMu.Unlock()
		session.mapEventAckMu.Lock()
		for sequence := range session.mapEventAcks {
			delete(session.mapEventAcks, sequence)
		}
		session.mapEventOrder = nil
		if session.mapEventWake != nil {
			close(session.mapEventWake)
			session.mapEventWake = nil
		}
		session.mapEventAckMu.Unlock()
		close(session.done)
		session.eventMu.Lock()
		session.eventClosing = true
		if session.eventCond == nil {
			session.eventCond = sync.NewCond(&session.eventMu)
		}
		for session.eventSenders > 0 {
			session.eventCond.Wait()
		}
		close(session.events)
		session.eventMu.Unlock()
	})
}

// Close cancels the network session and waits for its reader to terminate.
// It is safe to call repeatedly.
func (session *Session) Close() error {
	if session == nil {
		return nil
	}
	session.finish(ErrClosed)
	session.wg.Wait()
	return nil
}

func decodeEvent(packet []byte) (Event, error) {
	raw, err := namedproto.DecodePacket(packet)
	if err != nil {
		return Event{}, fmt.Errorf("%w: decode packet: %v", ErrProtocol, err)
	}
	message, err := namedproto.ParseMessage(raw)
	if err != nil {
		return Event{}, fmt.Errorf("%w: parse packet: %v", ErrProtocol, err)
	}
	schema := serverSchemas[message.Function]
	fields := make([]Field, len(message.Fields))
	for index, rawField := range message.Fields {
		fields[index] = Field{Kind: FieldRaw, Raw: rawField}
		if index >= len(schema) {
			continue
		}
		if schema[index] == wireInt {
			value, err := namedproto.DecodeInt(rawField)
			if err != nil {
				return Event{}, fmt.Errorf("%w: %s integer field %d: %v", ErrProtocol, message.Function, index, err)
			}
			fields[index] = Field{Kind: FieldInt, Raw: rawField, Int: value}
		} else {
			value, err := namedproto.DecodeString(rawField)
			if err != nil {
				return Event{}, fmt.Errorf("%w: %s string field %d: %v", ErrProtocol, message.Function, index, err)
			}
			fields[index] = Field{Kind: FieldString, Raw: rawField, Text: append([]byte(nil), value...)}
		}
	}
	return Event{ID: message.ID, Function: message.Function, Fields: fields, At: time.Now()}, nil
}

func responseText(event Event, index int) string {
	if index < 0 || index >= len(event.Fields) {
		return ""
	}
	return event.Fields[index].String()
}

func (session *Session) applyEvent(event Event) {
	session.stateMu.Lock()
	applyEventLocked(&session.state, event)
	session.state.snapshot.Revision++
	session.state.snapshot.At = event.At
	session.state.snapshot.LastFunction = event.Function
	session.stateMu.Unlock()
	if event.Function == "EV" && len(event.Fields) >= 2 {
		sequence := event.Fields[0].IntValue(0)
		if sequence > 0 {
			session.mapEventAckMu.Lock()
			if session.mapEventAcks == nil {
				session.mapEventAcks = make(map[int32]Event)
			}
			if session.mapEventWake == nil {
				session.mapEventWake = make(chan struct{})
			}
			if _, exists := session.mapEventAcks[sequence]; !exists {
				const maxMapEventAcks = 64
				if len(session.mapEventOrder) >= maxMapEventAcks {
					oldest := session.mapEventOrder[0]
					session.mapEventOrder = session.mapEventOrder[1:]
					delete(session.mapEventAcks, oldest)
				}
				session.mapEventOrder = append(session.mapEventOrder, sequence)
			}
			session.mapEventAcks[sequence] = cloneEvent(event)
			wake := session.mapEventWake
			// Close-and-replace broadcasts one ACK notification to every waiter;
			// a single buffered token would let an unrelated sequence consume the
			// wake and leave the matching waiter asleep until timeout.
			session.mapEventWake = make(chan struct{})
			close(wake)
			session.mapEventAckMu.Unlock()
		}
	}
}

// WaitForMapEvent waits for the server's EV(sequence,result) response. It is
// separate from Events because aiprovision consumes Events to wake the agent;
// waiting here therefore cannot race that wake consumer or lose an ACK which
// arrived before the movement executor started waiting.
func (session *Session) WaitForMapEvent(ctx context.Context, sequence int32) (Event, error) {
	if session == nil {
		return Event{}, ErrClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if sequence <= 0 {
		return Event{}, fmt.Errorf("%w: map event sequence must be positive", ErrInvalidAction)
	}
	for {
		if err := ctx.Err(); err != nil {
			return Event{}, err
		}
		session.mapEventAckMu.Lock()
		if event, ok := session.mapEventAcks[sequence]; ok {
			delete(session.mapEventAcks, sequence)
			for index, pending := range session.mapEventOrder {
				if pending == sequence {
					session.mapEventOrder = append(session.mapEventOrder[:index], session.mapEventOrder[index+1:]...)
					break
				}
			}
			session.mapEventAckMu.Unlock()
			return event, nil
		}
		wake := session.mapEventWake
		session.mapEventAckMu.Unlock()
		select {
		case <-ctx.Done():
			return Event{}, ctx.Err()
		case <-session.done:
			session.termMu.RLock()
			err := session.termErr
			session.termMu.RUnlock()
			if err == nil {
				err = ErrClosed
			}
			return Event{}, err
		case <-wake:
		}
	}
}

// cloneEvent and cloneSnapshot avoid exposing mutable buffers to callers.
func cloneEvent(event Event) Event {
	event.Fields = append([]Field(nil), event.Fields...)
	for index := range event.Fields {
		event.Fields[index].Text = append([]byte(nil), event.Fields[index].Text...)
	}
	return event
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	snapshot.Trade = cloneTradeSnapshot(snapshot.Trade)
	snapshot.Characters = append([]Character(nil), snapshot.Characters...)
	snapshot.Skills = append([]SkillSnapshot(nil), snapshot.Skills...)
	snapshot.Pets = clonePetSnapshots(snapshot.Pets)
	snapshot.AI.Pets = clonePetSnapshots(snapshot.AI.Pets)
	snapshot.AI.Items = append([]AIInventoryItem(nil), snapshot.AI.Items...)
	snapshot.Inventory = append([]InventoryItem(nil), snapshot.Inventory...)
	snapshot.Windows = append([]WindowSnapshot(nil), snapshot.Windows...)
	snapshot.Actors = append([]ActorSnapshot(nil), snapshot.Actors...)
	snapshot.Party = append([]PartyMember(nil), snapshot.Party...)
	snapshot.AddressBook = append([]AddressBookEntry(nil), snapshot.AddressBook...)
	snapshot.Chat = append([]ChatMessage(nil), snapshot.Chat...)
	snapshot.Battle.Participants = append([]BattleParticipant(nil), snapshot.Battle.Participants...)
	if snapshot.ActiveWindow != nil {
		window := *snapshot.ActiveWindow
		snapshot.ActiveWindow = &window
	}
	return snapshot
}

func sortAddressBook(values []AddressBookEntry) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j].Index < values[j-1].Index; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

func clonePetSnapshots(values []PetSnapshot) []PetSnapshot {
	cloned := append([]PetSnapshot(nil), values...)
	for index := range cloned {
		cloned[index].Skills = append([]PetSkillSnapshot(nil), values[index].Skills...)
	}
	return cloned
}

// base62Int decodes state tokens.  The server's status strings use decimal
// for full P1 values and base-62 for many masked fields; matching the web
// client's decimal-first stateNumber keeps both builds compatible.
func base62Int(value string, fallback int32) int32 {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	if parsed, err := strconv.ParseInt(value, 10, 32); err == nil {
		return int32(parsed)
	}
	parsed, err := namedproto.DecodeInt(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func splitPipe(value string) []string { return strings.Split(value, "|") }
