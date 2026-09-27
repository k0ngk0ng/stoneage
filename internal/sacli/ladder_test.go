package sacli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/ladder"
)

type ladderFixture struct {
	Game
	requests   []ladder.Request
	uncertain  bool
	historical bool
}

type ladderWaitFixture struct {
	ladderFixture
	stream string
	cursor uint64
}

func (g *ladderFixture) Observe(context.Context) (aigame.Snapshot, error) {
	return aigame.Snapshot{}, nil
}

func (g *ladderWaitFixture) WaitLadderEvents(ctx context.Context, stream string, cursor uint64) (ladder.Events, error) {
	g.stream, g.cursor = stream, cursor
	return ladder.Events{Stream: "new-connection", Cursor: 42, Gap: true, Snapshot: &ladder.Envelope{Snapshot: ladder.Snapshot{Phase: "battle"}}}, ctx.Err()
}

func TestLadderWaitRefreshesAfterReconnectAndPassesRecoveryCursor(t *testing.T) {
	g := &ladderWaitFixture{}
	s := &Server{game: g}
	response := s.commandLadderWait(context.Background(), []string{"15", "30s", "--stream", "old-connection"})
	if !response.OK || len(g.requests) != 1 || g.requests[0].Operation != "status" || g.stream != "old-connection" || g.cursor != 15 {
		t.Fatalf("%+v %+v", response, g)
	}
	var events ladder.Events
	if err := json.Unmarshal(response.Data, &events); err != nil || !events.Gap || events.Snapshot.Snapshot.Phase != "battle" {
		t.Fatalf("recovery: %s %v", response.Data, err)
	}
}

func TestLadderWaitCancellationIsNotASuccessfulTimeout(t *testing.T) {
	g := &ladderWaitFixture{}
	s := &Server{game: g}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	response := s.commandLadderWait(ctx, []string{"15", "--stream", "old-connection"})
	if response.OK || response.Kind != KindSession || !strings.Contains(response.Text, "canceled") {
		t.Fatalf("cancelled wait: %+v", response)
	}
}

func (g *ladderFixture) RequestLadder(_ context.Context, r ladder.Request) (ladder.Envelope, error) {
	g.requests = append(g.requests, r)
	if g.uncertain && r.Operation != "status" {
		return ladder.Envelope{}, errors.New("connection closed after write")
	}
	return ladder.Envelope{Version: 1, RequestID: r.ID, OK: true, Code: "ok", Revision: 42, Replay: g.historical, ServerBoot: "new", ReceiptBoot: "old", Snapshot: ladder.Snapshot{Phase: "lobby"}}, nil
}

func TestHistoricalReceiptDoesNotUndoLocalAutomationStop(t *testing.T) {
	for _, args := range [][]string{{"ready"}, {"strategy", "basic"}} {
		g := &ladderFixture{historical: true}
		s := &Server{game: g, autoLadderSuppressed: true}
		response := s.commandLadder(context.Background(), Request{Args: append(args, "--request-id", "old_request", "--revision", "10")})
		if !response.OK || !s.autoLadderSuppressed {
			t.Fatalf("historical request renewed automation: %+v", response)
		}
	}
}

func TestLadderCommandUsesServerRevisionAndPreservesUncertainRequest(t *testing.T) {
	g := &ladderFixture{uncertain: true}
	s := &Server{game: g}
	r := s.commandLadder(context.Background(), Request{Args: []string{"create", "3"}})
	if r.OK || len(g.requests) != 2 || g.requests[0].Operation != "status" || g.requests[1].Revision != 42 {
		t.Fatalf("%+v requests=%+v", r, g.requests)
	}
	if !strings.Contains(string(r.Data), `"code":"outcome_unknown"`) || !strings.Contains(string(r.Data), g.requests[1].ID) {
		t.Fatalf("missing reconciliation information: %s", r.Data)
	}
	id := g.requests[1].ID
	g.uncertain = false
	r = s.commandLadder(context.Background(), Request{Args: []string{"create", "3", "--request-id", id, "--revision", "42"}})
	if !r.OK || len(g.requests) != 3 || g.requests[2] != g.requests[1] {
		t.Fatalf("retry was not identical: %+v", g.requests)
	}
}

func TestInvalidLadderCommandNeverConnectsOrWrites(t *testing.T) {
	g := &ladderFixture{}
	s := &Server{game: g}
	for _, args := range [][]string{{"create", "6"}, {"queue", "--request-id", "id"}, {"accept", "spoof|ID"}, {"settle", "victory"}, {"invite", "0"}} {
		if r := s.commandLadder(context.Background(), Request{Args: args}); r.OK || r.Kind != KindUsage {
			t.Fatalf("args=%v response=%+v", args, r)
		}
	}
	if len(g.requests) != 0 {
		t.Fatal("invalid command reached server")
	}
}

func TestLadderInvitePreservesSelectedIdentityAndExactRetry(t *testing.T) {
	g := &ladderFixture{uncertain: true}
	s := &Server{game: g}
	r := s.commandLadder(context.Background(), Request{Args: []string{"invite", "3", "pc1_selected"}})
	if r.OK || len(g.requests) != 2 || g.requests[1].Argument != "3:pc1_selected" {
		t.Fatalf("%+v %+v", r, g.requests)
	}
	var unknown struct {
		Request ladder.Request `json:"request"`
	}
	if err := json.Unmarshal(r.Data, &unknown); err != nil {
		t.Fatal(err)
	}
	g.uncertain = false
	r = s.commandLadder(context.Background(), Request{Args: []string{"invite", unknown.Request.Argument, "--request-id", unknown.Request.ID, "--revision", "42"}})
	if !r.OK || g.requests[2] != unknown.Request {
		t.Fatalf("%+v %+v", r, g.requests)
	}
}

func TestLadderRecoveryResultPreservesUnknownStatisticsInJSONAndText(t *testing.T) {
	wire := `LADDER|{"version":1,"ok":true,"snapshot":{"phase":"result","result":{"id":"interrupted","reason":"server_restart","rated":false,"statistics_incomplete":true,"members":[{"id":"player","rating_before":1000,"rating_after":1000,"statistics":{}}]}}}`
	e, err := ladder.Decode(wire)
	if err != nil {
		t.Fatal(err)
	}
	r := ladderResponse(e)
	if !r.OK || !strings.Contains(r.Text, "were not recovered") || strings.Contains(r.Text, "damage=0") || strings.Contains(r.Text, "turns=0") {
		t.Fatalf("unknown statistics were displayed as measured zeros: %+v", r)
	}
	var decoded ladder.Envelope
	if err := json.Unmarshal(r.Data, &decoded); err != nil || !decoded.Snapshot.Result.StatisticsIncomplete {
		t.Fatalf("structured CLI result lost recovery metadata: %s (%v)", r.Data, err)
	}
}
