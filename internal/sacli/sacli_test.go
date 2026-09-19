package sacli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aiplanner"
)

// The transport decides which endpoint a session talks to, so a config that
// cannot carry a session must be refused before the daemon starts.
func TestConfigTransportValidation(t *testing.T) {
	directory := t.TempDir()
	write := func(body string) string {
		path := filepath.Join(directory, "sactl.toml")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	if _, err := LoadConfig(write("account = \"a\"\ntransport = \"http\"\n")); err == nil {
		t.Error("http transport without web_base_url was accepted")
	}
	if _, err := LoadConfig(write("account = \"a\"\ntransport = \"carrier-pigeon\"\n")); err == nil {
		t.Error("unknown transport was accepted")
	}
	// The defaults carry a local gateway address, so the check only fires when
	// a config explicitly clears it.
	if _, err := LoadConfig(write("account = \"a\"\ntransport = \"tcp\"\naddress = \"\"\n")); err == nil {
		t.Error("tcp transport without an address was accepted")
	}
	config, err := LoadConfig(write("account = \"a\"\ntransport = \"http\"\nweb_base_url = \"https://example.test\"\n"))
	if err != nil {
		t.Fatalf("http transport: %v", err)
	}
	if config.Transport != "http" || config.WebBaseURL != "https://example.test" {
		t.Fatalf("http config = %+v", config)
	}
	// An omitted transport keeps the direct gateway behaviour.
	config, err = LoadConfig(write("account = \"a\"\naddress = \"127.0.0.1:9065\"\n"))
	if err != nil || config.Transport != "tcp" {
		t.Fatalf("default transport = %q (%v)", config.Transport, err)
	}
}

// The wire alphabet is easy to get wrong: the preserved client encodes a
// direction through cnvServDir()'s rotation (client/web/index.html:1669-1676)
// and the navigator documents the same result (internal/ainavigation/types.go:87-90).
// These cases pin the mapping so a plausible-looking rewrite cannot shift it.
func TestDirectionLetterMatchesWireAlphabet(t *testing.T) {
	cases := []struct {
		name   string
		letter string
		deltaX int
		deltaY int
	}{
		{"up", "a", 0, -1},
		{"up-right", "b", 1, -1},
		{"right", "c", 1, 0},
		{"down-right", "d", 1, 1},
		{"down", "e", 0, 1},
		{"down-left", "f", -1, 1},
		{"left", "g", -1, 0},
		{"up-left", "h", -1, -1},
	}
	for _, testCase := range cases {
		letter, err := directionLetter(testCase.name)
		if err != nil {
			t.Fatalf("%s: %v", testCase.name, err)
		}
		if letter != testCase.letter {
			t.Errorf("%s: letter = %q, want %q", testCase.name, letter, testCase.letter)
		}
		deltaX, deltaY := directionDelta(letter)
		if deltaX != testCase.deltaX || deltaY != testCase.deltaY {
			t.Errorf("%s: delta = (%d,%d), want (%d,%d)", testCase.name, deltaX, deltaY, testCase.deltaX, testCase.deltaY)
		}
	}
}

// The wire letters themselves are not accepted as input: "e" is the wire
// letter for down while "east" is the compass name, so only the documented
// names and abbreviations are legal.
func TestDirectionLetterRejectsBareWireLetters(t *testing.T) {
	for _, input := range []string{"a", "b", "c", "d", "f", "g", "h", "upup", ""} {
		if _, err := directionLetter(input); err == nil {
			t.Errorf("directionLetter(%q) accepted a bare wire letter", input)
		}
	}
	for _, input := range []string{"n", "ne", "e", "se", "s", "sw", "w", "nw", "NORTH", " West "} {
		if _, err := directionLetter(input); err != nil {
			t.Errorf("directionLetter(%q) rejected a documented name: %v", input, err)
		}
	}
}

func TestParseSayArgs(t *testing.T) {
	text, color, chatRange := parseSayArgs([]string{"hello", "world"})
	if text != "hello world" || color != DefaultChatColor || chatRange != DefaultChatRange {
		t.Fatalf("plain text: got (%q,%d,%d)", text, color, chatRange)
	}
	text, color, chatRange = parseSayArgs([]string{"hi", "--range", "5", "--color", "2"})
	if text != "hi" || color != 2 || chatRange != 5 {
		t.Fatalf("flags: got (%q,%d,%d)", text, color, chatRange)
	}
	// A flag without a numeric value stays part of the message.
	text, _, _ = parseSayArgs([]string{"look", "--range", "far"})
	if text != "look --range far" {
		t.Fatalf("invalid flag value: got %q", text)
	}
}

func TestRequireWorldRejectsCharacterList(t *testing.T) {
	if err := requireWorld(aigame.Snapshot{Phase: aigame.PhaseCharacterList}); err == nil {
		t.Fatal("character-list phase must not allow world actions")
	}
	if err := requireWorld(aigame.Snapshot{Phase: aigame.PhaseWorld}); err != nil {
		t.Fatalf("world phase: %v", err)
	}
}

func TestConfigDefaultsAndPasswordFile(t *testing.T) {
	config, err := LoadConfig("")
	if err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if config.SocketPath != DefaultConfig().SocketPath || config.Address != DefaultConfig().Address {
		t.Fatalf("unexpected defaults: %+v", config)
	}

	directory := t.TempDir()
	passwordPath := filepath.Join(directory, "password")
	if err := os.WriteFile(passwordPath, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, "sactl.toml")
	body := "address = \"127.0.0.1:9065\"\naccount = \"sactl\"\npassword_file = \"" + passwordPath + "\"\n"
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Password != "secret" {
		t.Fatalf("password file not trimmed into Password: %q", loaded.Password)
	}
	if loaded.SocketPath != DefaultConfig().SocketPath {
		t.Fatalf("defaults must survive a partial config: %+v", loaded)
	}
}

func TestEventLogKeepsTailAndWakesWaiters(t *testing.T) {
	log := newEventLog(3)
	for index := 1; index <= 5; index++ {
		log.append(aigame.Event{Sequence: uint64(index), Function: "C"})
	}
	entries, notify := log.tail(2)
	if len(entries) != 2 || entries[0].Sequence != 4 || entries[1].Sequence != 5 {
		t.Fatalf("tail returned %+v", entries)
	}
	if log.lastSequence() != 5 {
		t.Fatalf("lastSequence = %d", log.lastSequence())
	}
	select {
	case <-notify:
		t.Fatal("notify must stay open until a new event arrives")
	default:
	}
	log.append(aigame.Event{Sequence: 6, Function: "TK"})
	select {
	case <-notify:
	case <-time.After(time.Second):
		t.Fatal("notify did not close on a new event")
	}
}

func TestDispatchUnknownCommandIsUsageError(t *testing.T) {
	server := NewServer(DefaultConfig())
	response := server.Dispatch(context.Background(), Request{Command: "fly"})
	if response.OK || response.Kind != KindUsage {
		t.Fatalf("unknown command: %+v", response)
	}
	if ping := server.Dispatch(context.Background(), Request{Command: "ping"}); !ping.OK {
		t.Fatalf("ping: %+v", ping)
	}
}

// The observation text is what a model reads; it must state the facts an
// action decision depends on and stay quiet about absent ones.
func TestRenderSnapshotReportsActionableFacts(t *testing.T) {
	snapshot := aigame.Snapshot{
		Phase:     aigame.PhaseWorld,
		Character: "SactlHero",
		Revision:  42,
		Position:  aigame.Point{Floor: 1006, X: 14, Y: 22, Direction: 3},
		Player:    aigame.PlayerSnapshot{HasStatus: true, Level: 1, HP: 35, MaxHP: 35, MP: 100, MaxMP: 100, Gold: 100},
		Actors: []aigame.ActorSnapshot{
			{ID: 7, Kind: "character", Name: "村长", X: 18, Y: 22, Level: 1},
		},
	}
	text := renderSnapshot(snapshot, "")
	for _, want := range []string{"phase=world", "position floor=1006 x=14 y=22", "player level=1 hp=35/35", "村长", "id=7"} {
		if !contains(text, want) {
			t.Errorf("rendered observation is missing %q:\n%s", want, text)
		}
	}
	empty := renderSnapshot(aigame.Snapshot{Phase: aigame.PhaseDisconnected}, "")
	if contains(empty, "position") {
		t.Errorf("unknown position must not be rendered: %s", empty)
	}
}

// The server never reports an owner's own walk, so the local actor record for
// the player keeps a stale tile. The observation must not print it next to
// the confirmed position.
func TestRenderSnapshotExcludesStaleSelfActor(t *testing.T) {
	snapshot := aigame.Snapshot{
		Phase:     aigame.PhaseWorld,
		Character: "SactlHero",
		Position:  aigame.Point{Floor: 1006, X: 16, Y: 13},
		Player:    aigame.PlayerSnapshot{ID: 5024, HasStatus: true},
		Actors: []aigame.ActorSnapshot{
			{ID: 5024, Kind: "character", Name: "SactlHero", X: 18, Y: 21},
			{ID: 28, Kind: "character", Name: "战斗指导员", X: 15, Y: 13},
		},
	}
	text := renderSnapshot(snapshot, "")
	if contains(text, "x=18 y=21") {
		t.Errorf("stale self position leaked into the observation:\n%s", text)
	}
	if !contains(text, "nearby=1") || !contains(text, "战斗指导员") {
		t.Errorf("other actors must still be listed:\n%s", text)
	}
	if !contains(text, "player_id=5024") {
		t.Errorf("the player's own id must stay available:\n%s", text)
	}
}

// Chat is a CP936 field; the length the client reports must be the byte
// length the server will see, not the Go rune or string length.
func TestChatLimitUsesLegacyBytes(t *testing.T) {
	encoded, err := encodeLegacy("你好")
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) != 4 {
		t.Fatalf("two Chinese characters encoded to %d bytes, want 4", len(encoded))
	}
	short, err := encodeLegacy("hi")
	if err != nil || len(short) != 2 {
		t.Fatalf("ascii: %d bytes, %v", len(short), err)
	}
}

// This payload is the one the family manager NPC actually sent during the
// 2026-09-18 local run. The leading selector counts wrapped message rows, the
// blank row keeps its position, and the wire value of each choice is its row
// number counted from the first selectable row — so the rendered numbers must
// start at 2, exactly as the native hit-test sends them.
func TestParseWindowSplitsMessageAndChoices(t *testing.T) {
	window := &aigame.WindowSnapshot{
		Type: 2, Sequence: 101, ObjectID: 56, Open: true,
		Data: "2\n我是这个村子里的家族管理员！\n有什么我可以为你服务的吗？\n\n介绍家族功能\n申请成立新的家族\n申请加入现有家族\n退出或者解散家族",
	}
	view := parseWindow(window)
	if len(view.Messages) != 2 || view.Messages[0] != "我是这个村子里的家族管理员！" {
		t.Fatalf("messages = %#v", view.Messages)
	}
	wantChoices := []string{"介绍家族功能", "申请成立新的家族", "申请加入现有家族", "退出或者解散家族"}
	if len(view.Choices) != len(wantChoices) {
		t.Fatalf("choices = %#v", view.Choices)
	}
	for index, want := range wantChoices {
		if view.Choices[index] != want {
			t.Errorf("choice %d = %q, want %q", index, view.Choices[index], want)
		}
		if view.ChoiceIndex[index] != index+2 {
			t.Errorf("choice %d wire value = %d, want %d", index, view.ChoiceIndex[index], index+2)
		}
	}
}

// Window data carries a second escape layer; unescaping it wrongly would show
// the reader backslashes and shift every choice.
func TestParseWindowUnescapesLegacyPayload(t *testing.T) {
	window := &aigame.WindowSnapshot{Type: 1, Sequence: 7, ObjectID: 9, Open: true,
		Data: `1\n你好\z欢迎\n\n是的\c当然`}
	view := parseWindow(window)
	if len(view.Messages) != 1 || view.Messages[0] != "你好|欢迎" {
		t.Fatalf("messages = %#v", view.Messages)
	}
	if len(view.Choices) != 1 || view.Choices[0] != "是的,当然" {
		t.Fatalf("choices = %#v", view.Choices)
	}
}

// A window without a selector digit has no choice rows; it must be shown as
// body text rather than guessed at.
func TestParseWindowWithoutSelectorIsBodyText(t *testing.T) {
	window := &aigame.WindowSnapshot{Type: 0, Sequence: 3, ObjectID: 4, Open: true, Data: "服务器窗口"}
	view := parseWindow(window)
	if len(view.Choices) != 0 || view.Body == "" {
		t.Fatalf("view = %#v", view)
	}
}

// A window can offer fewer rows than its numbers suggest, because blank rows
// still advance the wire value. A row number the window does not offer must be
// rejected, and one it does offer must be accepted even when it is larger than
// the number of rendered choices (this exact case rejected row 6 of a 5-row
// window during the 2026-09-18 Codex run).
func TestWindowRowNumbersFollowTheServerNotTheRenderedCount(t *testing.T) {
	window := &aigame.WindowSnapshot{
		Type: 1, Sequence: 5, ObjectID: 6, Open: true,
		Data: "1\n请选择\n\n甲\n乙\n丙\n丁\n戊",
	}
	view := parseWindow(window)
	if len(view.Choices) != 5 {
		t.Fatalf("choices = %#v", view.Choices)
	}
	if view.ChoiceIndex[0] != 2 || view.ChoiceIndex[4] != 6 {
		t.Fatalf("wire values = %#v, want 2..6", view.ChoiceIndex)
	}
	offered := func(row int) bool {
		for _, index := range view.ChoiceIndex {
			if index == row {
				return true
			}
		}
		return false
	}
	if !offered(6) {
		t.Error("row 6 is shown to the reader and must be accepted")
	}
	if offered(7) {
		t.Error("row 7 is not offered and must be rejected")
	}
}

// The client direction frame and its +5 rotation decide whether the character
// faces the NPC it is about to talk to.
func TestWireDirectionRotation(t *testing.T) {
	cases := []struct {
		deltaX, deltaY int
		client         int
		wire           int32
	}{
		{0, -1, 3, 0},  // up
		{1, -1, 4, 1},  // up-right
		{1, 0, 5, 2},   // right
		{1, 1, 6, 3},   // down-right
		{0, 1, 7, 4},   // down
		{-1, 1, 0, 5},  // down-left
		{-1, 0, 1, 6},  // left
		{-1, -1, 2, 7}, // up-left
	}
	for _, testCase := range cases {
		client, ok := clientDirectionFor(testCase.deltaX, testCase.deltaY)
		if !ok || client != testCase.client {
			t.Errorf("delta (%d,%d) -> client %d, want %d", testCase.deltaX, testCase.deltaY, client, testCase.client)
			continue
		}
		if wire := wireDirection(client); wire != testCase.wire {
			t.Errorf("client %d -> wire %d, want %d", client, wire, testCase.wire)
		}
	}
}

// A warp edge carries a time gate; using the wrong map event type makes the
// server reject the warp, so the mapping is pinned here.
func TestMapEventTypeForTime(t *testing.T) {
	cases := map[string]int32{
		"":        aigame.MapEventWarp,
		"NULL":    aigame.MapEventWarp,
		"ANY":     aigame.MapEventWarp,
		"M":       aigame.MapEventWarpMorning,
		"morning": aigame.MapEventWarpMorning,
		"A":       aigame.MapEventWarpNoon,
		"N":       aigame.MapEventWarpNight,
	}
	for selector, want := range cases {
		got, err := mapEventTypeForTime(selector)
		if err != nil {
			t.Errorf("%q: %v", selector, err)
			continue
		}
		if got != want {
			t.Errorf("%q -> %d, want %d", selector, got, want)
		}
	}
	if _, err := mapEventTypeForTime("X"); err == nil {
		t.Error("an unknown time selector must be rejected")
	}
}

// Walking to an exit needs a warp on the current floor that leads to the
// requested floor; EdgesFrom only matches an exact tile.
func TestSelectWarpToFloor(t *testing.T) {
	graph := aiplanner.NewWarpGraphFromWarps([]aiknowledge.MapWarp{
		{Type: "NONE", Time: "NULL", From: aiknowledge.Point{Floor: 1006, X: 10, Y: 20}, To: aiknowledge.Point{Floor: 1000, X: 98, Y: 44}},
		{Type: "NONE", Time: "NULL", From: aiknowledge.Point{Floor: 1000, X: 98, Y: 44}, To: aiknowledge.Point{Floor: 1006, X: 10, Y: 20}},
	})
	here := aiknowledge.Point{Floor: 1006, X: 18, Y: 25}
	edge, ok := selectWarpToFloor(graph, here, 1000)
	if !ok {
		t.Fatal("no exit found on floor 1006 leading to 1000")
	}
	if edge.From.X != 10 || edge.From.Y != 20 {
		t.Fatalf("picked the wrong exit: %+v", edge.From)
	}
	if _, ok := selectWarpToFloor(graph, here, 4242); ok {
		t.Fatal("a floor with no exit must not produce an edge")
	}
	if edges, err := graph.EdgesFrom(here, aiplanner.TimeAny); err != nil || len(edges) != 0 {
		t.Fatalf("EdgesFrom must match an exact tile, got %d edges (%v)", len(edges), err)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for index := 0; index+len(needle) <= len(haystack); index++ {
		if haystack[index:index+len(needle)] == needle {
			return index
		}
	}
	return -1
}
