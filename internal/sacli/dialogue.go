package sacli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

// clientDirections mirrors DIRS in the preserved web client
// (client/web/runtimeassets/index.html:1606): index 0 is down-left and 3 is up. The wire
// direction is this index rotated by +5 (cnvServDir/serverDirectionFromClient).
var clientDirections = [8][2]int{{-1, 1}, {-1, 0}, {-1, -1}, {0, -1}, {1, -1}, {1, 0}, {1, 1}, {0, 1}}

// talkFacingDelay is the pause the preserved client keeps between LOOK and TK
// so CHAR_Look has updated CHAR_DIR before NPC_Util_isFaceToFace() runs
// (client/web/runtimeassets/index.html:15531-15542).
const talkFacingDelay = 80 * time.Millisecond

// windowReplyTimeout bounds how long `talk` waits for the NPC window.
const windowReplyTimeout = 3 * time.Second

func sign(value int) int {
	switch {
	case value > 0:
		return 1
	case value < 0:
		return -1
	default:
		return 0
	}
}

// clientDirectionFor returns the client direction index toward a delta.
func clientDirectionFor(deltaX, deltaY int) (int, bool) {
	stepX, stepY := sign(deltaX), sign(deltaY)
	for index, delta := range clientDirections {
		if delta[0] == stepX && delta[1] == stepY {
			return index, true
		}
	}
	return 0, false
}

// wireDirection rotates a client direction into the server's direction value.
func wireDirection(clientDirection int) int32 {
	return int32(((clientDirection+5)%8 + 8) % 8)
}

// windowButtons names each response bit the way the browser client draws it
// (client/web/runtimeassets/index.html:1688, WINDOW_BUTTONS). Bits 4 and 8 are yes/no, not a
// second 确定/取消 pair: a narrative window offering only bit 4 answers to
// `reply yes`, and labelling it 确定 sent the caller to `reply ok`, which the
// window correctly refuses.
var windowButtons = []struct {
	Bit  int32
	Name string
}{
	{1, "确定"},
	{2, "取消"},
	{4, "是"},
	{8, "否"},
	{16, "上一页"},
	{32, "下一页"},
}

// buttonNames maps the names accepted by `reply` to server button bits.
var buttonNames = map[string]int32{
	"ok": 1, "确定": 1, "confirm": 1,
	"cancel": 2, "取消": 2, "esc": 2,
	"yes": 4, "是": 4,
	"no": 8, "否": 8,
	"prev": 16, "上一页": 16,
	"next": 32, "下一页": 32,
}

// windowView is the readable projection of one server window.
type windowView struct {
	Type       int32
	Sequence   int32
	ObjectID   int32
	ButtonType int32
	Submitted  bool
	Messages   []string
	Choices    []string
	// ChoiceIndex holds the wire value to submit for each rendered choice.
	ChoiceIndex []int
	Body        string
}

// windowUnescape reverts the legacy escape layer carried inside window data
// (client/web/runtimeassets/index.html:15968-15978). The outer protocol decoder has already
// removed its own quoting by the time the payload reaches us.
func windowUnescape(value string) string {
	replaced := strings.NewReplacer(`\y`, `\`, `\z`, `|`, `\n`, "\n", `\c`, ",").Replace(value)
	if strings.HasSuffix(replaced, "|0") {
		replaced = replaced[:len(replaced)-2]
	}
	return strings.ReplaceAll(replaced, "\r", "")
}

// wrapWindowRow wraps one physical row at the client's 40-byte limit, counting
// non-ASCII characters as two bytes (LOGIN.CPP getStrSplit(..., 40)).
func wrapWindowRow(row string) []string {
	const maxBytes = 40
	rows := make([]string, 0, 1)
	current := ""
	bytes := 0
	for _, character := range row {
		width := 1
		if character > 0x7f {
			width = 2
		}
		if current != "" && bytes+width > maxBytes {
			rows = append(rows, current)
			current, bytes = "", 0
		}
		current += string(character)
		bytes += width
	}
	return append(rows, current)
}

// parseWindow projects a server window into messages and choices. The first
// row may be a one-digit selector naming how many wrapped rows are message
// text; every later row is a selectable row, and its wire value is its row
// number counted from the first selectable row (including blank rows), which
// is what the native hit-test sends (client/web/runtimeassets/index.html:15015-15033).
func parseWindow(window *aigame.WindowSnapshot) *windowView {
	if window == nil {
		return nil
	}
	view := &windowView{
		Type:       window.Type,
		Sequence:   window.Sequence,
		ObjectID:   window.ObjectID,
		ButtonType: window.ButtonType,
		Submitted:  window.Submitted,
	}
	text := windowUnescape(window.Data)
	startLine := -1
	if len(text) >= 2 && text[1] == '\n' && text[0] >= '0' && text[0] <= '7' {
		startLine = int(text[0] - '0')
		text = text[2:]
	}
	rows := make([]string, 0, 16)
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			rows = append(rows, "")
			continue
		}
		rows = append(rows, wrapWindowRow(line)...)
	}
	if startLine < 0 || startLine > len(rows) {
		view.Body = strings.Join(rows, "\n")
		return view
	}
	view.Messages = append([]string(nil), rows[:startLine]...)
	for index, row := range rows[startLine:] {
		if strings.TrimSpace(row) == "" {
			continue
		}
		view.Choices = append(view.Choices, row)
		view.ChoiceIndex = append(view.ChoiceIndex, index+1)
	}
	return view
}

// renderWindow appends the readable form of a server window.
func renderWindow(builder *strings.Builder, window *aigame.WindowSnapshot) {
	view := parseWindow(window)
	if view == nil || !window.Open {
		return
	}
	buttons := windowButtonNames(view.ButtonType)
	fmt.Fprintf(builder, "window type=%d sequence=%d object=%d submitted=%t", view.Type, view.Sequence, view.ObjectID, view.Submitted)
	if buttons != "" {
		fmt.Fprintf(builder, " buttons=%s", buttons)
	}
	builder.WriteString("\n")
	if len(view.Messages) > 0 {
		builder.WriteString("  message:\n")
		for _, line := range view.Messages {
			fmt.Fprintf(builder, "    %s\n", line)
		}
	}
	if view.Body != "" {
		for _, line := range strings.Split(view.Body, "\n") {
			fmt.Fprintf(builder, "  %s\n", line)
		}
	}
	if len(view.Choices) > 0 {
		builder.WriteString("  choices:\n")
		for index, choice := range view.Choices {
			fmt.Fprintf(builder, "    %d) %s\n", view.ChoiceIndex[index], choice)
		}
	}
}

func windowButtonNames(buttonType int32) string {
	if buttonType == 0 {
		return ""
	}
	names := make([]string, 0, 4)
	for _, button := range windowButtons {
		if buttonType&button.Bit != 0 {
			names = append(names, button.Name)
		}
	}
	return strings.Join(names, "|")
}

// findTalkTarget locates the visible character the caller asked for. An exact
// name wins; otherwise a unique substring match is accepted so a model can say
// "村长" for "萨姆吉尔的村长".
func findTalkTarget(snapshot aigame.Snapshot, name string) (aigame.ActorSnapshot, error) {
	query := strings.ToLower(strings.TrimSpace(name))
	if query == "" {
		return aigame.ActorSnapshot{}, fmt.Errorf("talk: empty target name")
	}
	candidates := make([]aigame.ActorSnapshot, 0, len(snapshot.Actors))
	for _, actor := range snapshot.Actors {
		if actor.Kind != "character" && actor.CharType != 1 {
			continue
		}
		if snapshot.Player.ID != 0 && actor.ID == snapshot.Player.ID {
			continue
		}
		if snapshot.Character != "" && actor.Name == snapshot.Character {
			continue
		}
		label := strings.ToLower(actor.Name)
		if label == "" {
			label = strings.ToLower(actor.FreeName)
		}
		if label == query {
			return actor, nil
		}
		if strings.Contains(label, query) {
			candidates = append(candidates, actor)
		}
	}
	switch len(candidates) {
	case 0:
		return aigame.ActorSnapshot{}, fmt.Errorf("talk: no visible character matching %q; use `sactl observe` to see who is nearby", name)
	case 1:
		return candidates[0], nil
	default:
		names := make([]string, 0, len(candidates))
		for _, actor := range candidates {
			names = append(names, fmt.Sprintf("%q at (%d,%d)", actor.Name, actor.X, actor.Y))
		}
		return aigame.ActorSnapshot{}, fmt.Errorf("talk: %q matches several characters: %s", name, strings.Join(names, ", "))
	}
}

// faceActor turns the character toward one visible actor and lets the server
// apply the facing. Face-to-face checks (talk, trade request) are decided
// server-side from CHAR_DIR, so the pause matters
// (client/web/runtimeassets/index.html:15531-15542).
func (s *Server) faceActor(ctx context.Context, snapshot aigame.Snapshot, target aigame.ActorSnapshot) error {
	clientDirection, ok := clientDirectionFor(int(target.X-snapshot.Position.X), int(target.Y-snapshot.Position.Y))
	if !ok {
		return fmt.Errorf("%q is not on a legal direction from (%d,%d)", target.Name, snapshot.Position.X, snapshot.Position.Y)
	}
	if err := s.submit(ctx, snapshot.Revision, aigame.Look(wireDirection(clientDirection))); err != nil {
		return err
	}
	time.Sleep(talkFacingDelay)
	return nil
}

// commandTalk faces a nearby character and speaks to it, then reports the
// window the server sent back.
func (s *Server) commandTalk(ctx context.Context, request Request) Response {
	if len(request.Args) == 0 {
		return failure(KindUsage, "usage: sactl talk <name> [text]")
	}
	name := request.Args[0]
	text := "hi"
	if len(request.Args) > 1 {
		text = strings.Join(request.Args[1:], " ")
	}
	if encoded, err := encodeLegacy("P|" + text); err != nil {
		return failure(KindUsage, "talk: text cannot be encoded for the legacy client: %v", err)
	} else if len(encoded) > 70 {
		return failure(KindUsage, "talk: text is %d bytes but the talk field allows 70; shorten it", len(encoded))
	}

	snapshot, err := s.snapshot(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	if err := requireWorld(snapshot); err != nil {
		return actionFailure(err)
	}
	target, err := findTalkTarget(snapshot, name)
	if err != nil {
		return actionFailure(err)
	}
	distance := maxInt32(absInt32(target.X-snapshot.Position.X), absInt32(target.Y-snapshot.Position.Y))
	if distance == 0 || distance > 1 {
		return actionFailure(fmt.Errorf("talk: %q is %d tiles away at (%d,%d); walk next to it first (you are at (%d,%d))",
			target.Name, distance, target.X, target.Y, snapshot.Position.X, snapshot.Position.Y))
	}
	if err := s.faceActor(ctx, snapshot, target); err != nil {
		return actionFailure(err)
	}

	current, err := s.snapshot(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	if err := s.submit(ctx, current.Revision, aigame.Talk(current.Position.X, current.Position.Y, "P|"+text, DefaultChatColor, DefaultChatRange)); err != nil {
		return executeFailure(err)
	}

	// A 2.5 NPC with no TalkedFunc legitimately answers nothing, so a missing
	// window is reported as such instead of being treated as a failure.
	deadline := time.Now().Add(windowReplyTimeout)
	for {
		time.Sleep(150 * time.Millisecond)
		after, err := s.snapshot(ctx)
		if err != nil {
			return sessionFailure(err)
		}
		if window := after.ActiveWindow; window != nil && window.Open && !window.Submitted {
			return Response{
				OK:   true,
				Text: fmt.Sprintf("talked to %q\n%s", target.Name, s.renderObservation(after)),
				Data: replyJSON(after),
			}
		}
		if time.Now().After(deadline) {
			return Response{
				OK: true,
				Text: fmt.Sprintf("talked to %q, but no window arrived within %s (this NPC may have no dialogue). Current observation:\n%s",
					target.Name, windowReplyTimeout, s.renderObservation(after)),
				Data: replyJSON(after),
			}
		}
	}
}

// commandChoose answers a select window by row number.
func (s *Server) commandChoose(ctx context.Context, request Request) Response {
	if len(request.Args) != 1 {
		return failure(KindUsage, "usage: sactl choose <row> (the number shown in the window's choices)")
	}
	row, err := strconv.Atoi(request.Args[0])
	if err != nil || row < 1 {
		return failure(KindUsage, "choose: row must be a positive number")
	}
	snapshot, err := s.snapshot(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	window := snapshot.ActiveWindow
	if window == nil || !window.Open || window.Submitted {
		return actionFailure(fmt.Errorf("choose: there is no open window; run `sactl observe` first"))
	}
	// The rendered number IS the wire value, and it counts blank rows, so it
	// can be larger than the number of choices shown. Validate membership in
	// the offered set rather than comparing with its size.
	view := parseWindow(window)
	if view != nil && len(view.ChoiceIndex) > 0 {
		offered := false
		for _, index := range view.ChoiceIndex {
			if index == row {
				offered = true
				break
			}
		}
		if !offered {
			return actionFailure(fmt.Errorf("choose: row %d is not one of the rows this window offers (%s); the numbers to use are the ones shown in `sactl observe`",
				row, joinChoices(view)))
		}
	}
	if err := s.submit(ctx, snapshot.Revision, aigame.Window(
		snapshot.Position.X, snapshot.Position.Y, window.Sequence, window.ObjectID, 0, strconv.Itoa(row))); err != nil {
		return executeFailure(err)
	}
	return s.reportWindowChange(ctx, fmt.Sprintf("chose row %d", row), window.Sequence)
}

// commandReply answers a message window by button name.
func (s *Server) commandReply(ctx context.Context, request Request) Response {
	if len(request.Args) == 0 {
		return failure(KindUsage, "usage: sactl reply <ok|cancel|yes|no|prev|next> [text]")
	}
	bit, ok := buttonNames[strings.ToLower(request.Args[0])]
	if !ok {
		if number, err := strconv.Atoi(request.Args[0]); err == nil {
			bit = int32(number)
		} else {
			return failure(KindUsage, "reply: unknown button %q (use ok, cancel, yes, no, prev, next or a raw bit)", request.Args[0])
		}
	}
	text := ""
	if len(request.Args) > 1 {
		text = strings.Join(request.Args[1:], " ")
	}
	snapshot, err := s.snapshot(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	window := snapshot.ActiveWindow
	if window == nil || !window.Open || window.Submitted {
		return actionFailure(fmt.Errorf("reply: there is no open window; run `sactl observe` first"))
	}
	if window.ButtonType != 0 && window.ButtonType&bit == 0 {
		return actionFailure(fmt.Errorf("reply: the open window does not offer button %d (available: %s)",
			bit, windowButtonNames(window.ButtonType)))
	}
	if err := s.submit(ctx, snapshot.Revision, aigame.Window(
		snapshot.Position.X, snapshot.Position.Y, window.Sequence, window.ObjectID, bit, text)); err != nil {
		return executeFailure(err)
	}
	return s.reportWindowChange(ctx, fmt.Sprintf("replied with button %d", bit), window.Sequence)
}

// reportWindowChange waits briefly for the server to replace or close the
// answered window, so the caller sees what its answer caused.
func (s *Server) reportWindowChange(ctx context.Context, action string, previousSequence int32) Response {
	deadline := time.Now().Add(windowReplyTimeout)
	for {
		time.Sleep(150 * time.Millisecond)
		snapshot, err := s.snapshot(ctx)
		if err != nil {
			return sessionFailure(err)
		}
		window := snapshot.ActiveWindow
		changed := window == nil || !window.Open || window.Sequence != previousSequence || window.Submitted
		if changed {
			return Response{
				OK:   true,
				Text: fmt.Sprintf("%s\n%s", action, s.renderObservation(snapshot)),
				Data: replyJSON(snapshot),
			}
		}
		if time.Now().After(deadline) {
			return Response{
				OK:   true,
				Text: fmt.Sprintf("%s, but the server did not replace the window within %s\n%s", action, windowReplyTimeout, s.renderObservation(snapshot)),
				Data: replyJSON(snapshot),
			}
		}
	}
}

// joinChoices renders the offered rows for an error message.
func joinChoices(view *windowView) string {
	parts := make([]string, 0, len(view.Choices))
	for index, choice := range view.Choices {
		parts = append(parts, fmt.Sprintf("%d) %s", view.ChoiceIndex[index], choice))
	}
	return strings.Join(parts, " / ")
}

func absInt32(value int32) int32 {
	if value < 0 {
		return -value
	}
	return value
}

func maxInt32(left, right int32) int32 {
	if left > right {
		return left
	}
	return right
}
