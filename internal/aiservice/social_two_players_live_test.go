package aiservice

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/ainavigation"
	"github.com/k0ngk0ng/stoneage/internal/aiprovision"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

const socialTwoPlayersLiveOptIn = "STONEAGE_SOCIAL_TWO_FRESH_AI_LIVE_TEST"

// TestLiveSocialTwoFreshAI exercises the native two-player social path on the
// isolated QA server. It provisions both identities through one authenticated
// gateway, moves one character to a verified adjacent tile, and proves
// delivery by observing the peer session. A successful packet write is never
// treated as a chat or mail delivery acknowledgement.
func TestLiveSocialTwoFreshAI(t *testing.T) {
	if os.Getenv(socialTwoPlayersLiveOptIn) != "1" {
		t.Skip("set STONEAGE_SOCIAL_TWO_FRESH_AI_LIVE_TEST=1 for the two-player social QA check")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	fixture := movementCrossMapLiveProvisionFreshAI(t, ctx, "social-two", "aiservice-social-two-live-test")
	peerCreated, peerProfile, peerLease := socialTwoPlayersLiveProvisionPeer(t, ctx, fixture)
	t.Cleanup(func() {
		if peerLease.Close != nil {
			peerLease.Close()
		}
	})

	first := fixture.Lease.Session
	second := peerLease.Session
	if first == nil || second == nil {
		t.Fatal("two-player fixture returned an empty game session")
	}
	firstInitial, err := socialTwoPlayersLiveWaitWorld(ctx, first, fixture.Created.Account.Username, fixture.Created.Binding.CharacterName)
	if err != nil {
		t.Fatal("first fresh AI did not remain in the world:", err)
	}
	secondInitial, err := socialTwoPlayersLiveWaitWorld(ctx, second, peerCreated.Account.Username, peerCreated.Binding.CharacterName)
	if err != nil {
		t.Fatal("second fresh AI did not enter the world:", err)
	}
	if firstInitial.Position.Floor != secondInitial.Position.Floor {
		t.Fatalf("fresh AIs entered different floors: first=%+v second=%+v", firstInitial.Position, secondInitial.Position)
	}
	if firstInitial.Position.X != secondInitial.Position.X || firstInitial.Position.Y != secondInitial.Position.Y {
		t.Fatalf("fresh AIs did not share the expected hometown tile: first=%+v second=%+v", firstInitial.Position, secondInitial.Position)
	}

	navigator, err := ainavigation.LoadDataDir(ctx, filepath.Join(fixture.RepoRoot, "runtime", "legacy-server", "gmsv", "data"))
	if err != nil {
		t.Fatal("load verified hometown navigation:", err)
	}
	secondBeforeMove := secondInitial
	target, route, err := socialTwoPlayersLiveAdjacentRoute(ctx, navigator, secondBeforeMove)
	if err != nil {
		t.Fatal("find a verified unoccupied adjacent hometown tile:", err)
	}
	if int32(target.X) == firstInitial.Position.X && int32(target.Y) == firstInitial.Position.Y {
		t.Fatal("selected adjacent tile is occupied by the first AI")
	}
	if err := socialTwoPlayersLiveSubmit(ctx, second, aigame.Move(secondBeforeMove.Position.X, secondBeforeMove.Position.Y, route.Directions)); err != nil {
		t.Fatal("move second AI to the verified adjacent tile:", err)
	}
	secondAtTarget, err := socialTwoPlayersLiveWaitPosition(ctx, second, firstInitial.Position.Floor, int32(target.X), int32(target.Y))
	if err != nil {
		t.Fatal("second AI did not reach the verified adjacent tile:", err)
	}
	firstAtStart, err := socialTwoPlayersLiveWaitPosition(ctx, first, firstInitial.Position.Floor, firstInitial.Position.X, firstInitial.Position.Y)
	if err != nil {
		t.Fatal("first AI moved while positioning the peer:", err)
	}

	firstToSecond, ok := directionForDelta(secondAtTarget.Position.X-firstAtStart.Position.X, secondAtTarget.Position.Y-firstAtStart.Position.Y)
	if !ok {
		t.Fatalf("peer is not on a legal adjacent direction: first=%+v second=%+v", firstAtStart.Position, secondAtTarget.Position)
	}
	secondToFirst, ok := directionForDelta(firstAtStart.Position.X-secondAtTarget.Position.X, firstAtStart.Position.Y-secondAtTarget.Position.Y)
	if !ok {
		t.Fatalf("reverse peer direction is not legal: first=%+v second=%+v", firstAtStart.Position, secondAtTarget.Position)
	}
	if err := socialTwoPlayersLiveLook(ctx, first, firstToSecond); err != nil {
		t.Fatal("first AI did not face the second AI:", err)
	}
	if err := socialTwoPlayersLiveLook(ctx, second, secondToFirst); err != nil {
		t.Fatal("second AI did not face the first AI:", err)
	}

	if err := socialTwoPlayersLiveEnableTradeCard(ctx, first); err != nil {
		t.Fatal("enable first AI trade-card setting:", err)
	}
	if err := socialTwoPlayersLiveEnableTradeCard(ctx, second); err != nil {
		t.Fatal("enable second AI trade-card setting:", err)
	}

	publicFirstToSecond := "social-two-public-a-" + movementCrossMapLiveHex(t, 16)
	if err := socialTwoPlayersLiveSubmit(ctx, first, aigame.Chat(publicFirstToSecond, 0, 3)); err != nil {
		t.Fatal("submit first public chat:", err)
	}
	// CHAR_talkToCli prepends the speaker's name/title to public chat.
	// Match the unique body token inside that server-formatted message.
	if _, err := socialTwoPlayersLiveWaitChat(ctx, second, "P", publicFirstToSecond, true); err != nil {
		t.Fatal("second AI did not receive first public chat:", err)
	}
	publicSecondToFirst := "social-two-public-b-" + movementCrossMapLiveHex(t, 16)
	if err := socialTwoPlayersLiveSubmit(ctx, second, aigame.Chat(publicSecondToFirst, 0, 3)); err != nil {
		t.Fatal("submit second public chat:", err)
	}
	if _, err := socialTwoPlayersLiveWaitChat(ctx, first, "P", publicSecondToFirst, true); err != nil {
		t.Fatal("first AI did not receive second public chat:", err)
	}

	firstFacing, err := first.Observe(ctx)
	if err != nil {
		t.Fatal("observe first AI before AAB:", err)
	}
	if firstFacing.Position.Floor != secondAtTarget.Position.Floor || firstFacing.Position.X != firstAtStart.Position.X || firstFacing.Position.Y != firstAtStart.Position.Y || firstFacing.Position.Direction != firstToSecond {
		t.Fatalf("first AI lost the required adjacent facing before AAB: %+v", firstFacing.Position)
	}
	if err := socialTwoPlayersLiveSubmit(ctx, first, aigame.AddMailContact(firstFacing.Position.X, firstFacing.Position.Y)); err != nil {
		t.Fatal("submit native AAB:", err)
	}

	firstAB, err := socialTwoPlayersLiveRefreshAB(ctx, first, peerCreated.Binding.CharacterName)
	if err != nil {
		t.Fatal("first AI did not receive a new complete AB containing the peer:", err)
	}
	secondAB, err := socialTwoPlayersLiveRefreshAB(ctx, second, fixture.Created.Binding.CharacterName)
	if err != nil {
		t.Fatal("second AI did not receive a new complete AB containing the peer:", err)
	}
	firstIndex, err := socialTwoPlayersLiveAddressIndex(firstAB, peerCreated.Binding.CharacterName)
	if err != nil {
		t.Fatal("find first AI address-book slot for the peer:", err)
	}
	secondIndex, err := socialTwoPlayersLiveAddressIndex(secondAB, fixture.Created.Binding.CharacterName)
	if err != nil {
		t.Fatal("find second AI address-book slot for the peer:", err)
	}

	mailFirstToSecond := "social-two-mail-a-" + movementCrossMapLiveHex(t, 16)
	if err := socialTwoPlayersLiveSubmit(ctx, first, aigame.Mail(firstIndex, mailFirstToSecond, 0)); err != nil {
		t.Fatal("submit first MSG:", err)
	}
	if _, err := socialTwoPlayersLiveWaitChat(ctx, second, "msg", mailFirstToSecond, true); err != nil {
		t.Fatal("second AI did not receive first MSG:", err)
	}
	mailSecondToFirst := "social-two-mail-b-" + movementCrossMapLiveHex(t, 16)
	if err := socialTwoPlayersLiveSubmit(ctx, second, aigame.Mail(secondIndex, mailSecondToFirst, 0)); err != nil {
		t.Fatal("submit second MSG:", err)
	}
	if _, err := socialTwoPlayersLiveWaitChat(ctx, first, "msg", mailSecondToFirst, true); err != nil {
		t.Fatal("first AI did not receive second MSG:", err)
	}

	t.Logf("two-player social QA passed: first=%s second=%s floor=%d adjacent=(%d,%d) public_and_mail_bidirectional=true",
		fixture.Created.Binding.CharacterName, peerCreated.Binding.CharacterName,
		firstAtStart.Position.Floor, secondAtTarget.Position.X, secondAtTarget.Position.Y)
	t.Logf("peer profile persisted as %s", peerProfile.ID)
}

func socialTwoPlayersLiveProvisionPeer(t *testing.T, ctx context.Context, fixture *movementCrossMapLiveFreshAI) (aiprovision.Provisioned, airuntime.Profile, aiprovision.SessionLease) {
	t.Helper()
	if fixture == nil || fixture.Provisioner == nil || fixture.Profiles == nil || fixture.Provider == nil {
		t.Fatal("fresh AI fixture is missing shared provisioning dependencies")
	}
	characterName := "AIBot" + movementCrossMapLiveHex(t, 12)
	profileID := "social-two-peer-" + movementCrossMapLiveHex(t, 12)
	characterCreate := movementCrossMapLiveDefaultCharacterCreate()
	characterCreate.Hometown = 0
	created, err := fixture.Provisioner.CreateAI(ctx, aiprovision.CreateRequest{
		ProfileID:       profileID,
		CharacterSlot:   0,
		CharacterName:   characterName,
		CharacterCreate: characterCreate,
		Profile:         airuntime.Profile{Status: airuntime.ProfileStatusActive, UnlimitedFunds: true},
		Actor:           "aiservice-social-two-peer-live-test",
	})
	if err != nil {
		t.Fatal("CreateAI did not create the second fresh identity:", err)
	}
	profile, err := fixture.Profiles.GetProfile(ctx, created.Profile.ID)
	if err != nil {
		t.Fatal("second AI profile was not persisted:", err)
	}
	if profile.Account.Username != created.Account.Username || profile.Character.Name != characterName || !profile.UnlimitedFunds {
		t.Fatalf("second AI profile does not match CreateAI: profile=%+v created=%+v", profile, created)
	}
	lease, err := movementCrossMapLiveOpenWithRetry(ctx, fixture.Provider, profile)
	if err != nil {
		t.Fatal("Provider.Open could not enter the second fresh AI:", err)
	}
	return created, profile, lease
}

func socialTwoPlayersLiveSubmit(ctx context.Context, session aiprovision.HeadlessSession, action aigame.Action) error {
	if session == nil {
		return errors.New("nil game session")
	}
	before, err := session.Observe(ctx)
	if err != nil {
		return err
	}
	submitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return session.ExecuteExpected(submitCtx, before.Revision, action)
}

func socialTwoPlayersLiveWait(ctx context.Context, session aiprovision.HeadlessSession, predicate func(aigame.Snapshot) bool) (aigame.Snapshot, error) {
	if session == nil {
		return aigame.Snapshot{}, errors.New("nil game session")
	}
	waitCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return movementCrossMapLiveWaitSnapshot(waitCtx, session, predicate)
}

// socialTwoPlayersLiveWaitPosition confirms an authoritative position sample
// after a movement write. Observe may lag the server's own position packet, so
// the helper periodically asks for the read-only S c refresh. A stale revision
// means another event raced that refresh; observe again and never resend W.
func socialTwoPlayersLiveWaitPosition(ctx context.Context, session aiprovision.HeadlessSession, floor, x, y int32) (aigame.Snapshot, error) {
	if session == nil {
		return aigame.Snapshot{}, errors.New("nil game session")
	}
	waitCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	last, err := session.Observe(waitCtx)
	if err != nil {
		if waitCtx.Err() != nil {
			return last, socialTwoPlayersLivePositionTimeout(floor, x, y, last, waitCtx.Err())
		}
		return last, err
	}
	matches := func(snapshot aigame.Snapshot) bool {
		return snapshot.Phase == aigame.PhaseWorld && snapshot.Connected &&
			snapshot.Position.Floor == floor && snapshot.Position.X == x && snapshot.Position.Y == y
	}
	if matches(last) {
		return last, nil
	}

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		statusCtx, statusCancel := context.WithTimeout(waitCtx, 10*time.Second)
		err = session.ExecuteExpected(statusCtx, last.Revision, aigame.Action{Kind: aigame.ActionStatus, Command: "c"})
		statusCancel()
		if err != nil && !errors.Is(err, aigame.ErrStaleRevision) {
			if waitCtx.Err() != nil {
				return last, socialTwoPlayersLivePositionTimeout(floor, x, y, last, waitCtx.Err())
			}
			return last, err
		}
		if errors.Is(err, aigame.ErrStaleRevision) {
			last, err = session.Observe(waitCtx)
			if err != nil {
				if waitCtx.Err() != nil {
					return last, socialTwoPlayersLivePositionTimeout(floor, x, y, last, waitCtx.Err())
				}
				return last, err
			}
			if matches(last) {
				return last, nil
			}
		}

		select {
		case <-waitCtx.Done():
			return last, socialTwoPlayersLivePositionTimeout(floor, x, y, last, waitCtx.Err())
		case <-ticker.C:
		}
		last, err = session.Observe(waitCtx)
		if err != nil {
			if waitCtx.Err() != nil {
				return last, socialTwoPlayersLivePositionTimeout(floor, x, y, last, waitCtx.Err())
			}
			return last, err
		}
		if matches(last) {
			return last, nil
		}
	}
}

func socialTwoPlayersLivePositionTimeout(floor, x, y int32, last aigame.Snapshot, cause error) error {
	if cause == nil {
		cause = context.DeadlineExceeded
	}
	return fmt.Errorf("social position confirmation target floor=%d x=%d y=%d, last position floor=%d x=%d y=%d revision=%d: %w", floor, x, y, last.Position.Floor, last.Position.X, last.Position.Y, last.Revision, cause)
}

func socialTwoPlayersLiveWaitWorld(ctx context.Context, session aiprovision.HeadlessSession, account, character string) (aigame.Snapshot, error) {
	return socialTwoPlayersLiveWait(ctx, session, func(snapshot aigame.Snapshot) bool {
		return snapshot.Account == account && snapshot.Character == character && snapshot.Phase == aigame.PhaseWorld && snapshot.Connected && snapshot.Player.HasStatus && snapshot.Position.Floor >= 0
	})
}

func socialTwoPlayersLiveLook(ctx context.Context, session aiprovision.HeadlessSession, direction int32) error {
	if session == nil {
		return errors.New("nil game session")
	}
	before, err := session.Observe(ctx)
	if err != nil {
		return err
	}
	submitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = session.ExecuteExpected(submitCtx, before.Revision, aigame.Look(direction))
	cancel()
	if err != nil {
		return err
	}
	_, err = socialTwoPlayersLiveWait(ctx, session, func(snapshot aigame.Snapshot) bool {
		return snapshot.Phase == aigame.PhaseWorld && snapshot.Connected && snapshot.Position.Floor == before.Position.Floor && snapshot.Position.X == before.Position.X && snapshot.Position.Y == before.Position.Y && snapshot.Position.Direction == direction && snapshot.Revision > before.Revision
	})
	return err
}

func socialTwoPlayersLiveEnableTradeCard(ctx context.Context, session aiprovision.HeadlessSession) error {
	if session == nil {
		return errors.New("nil game session")
	}
	before, err := socialTwoPlayersLiveWait(ctx, session, func(snapshot aigame.Snapshot) bool {
		return snapshot.Phase == aigame.PhaseWorld && snapshot.Connected && snapshot.Player.SocialFlagsKnown
	})
	if err != nil {
		return fmt.Errorf("social flags unavailable: %w", err)
	}
	const tradeCardMask int32 = 1 << 4
	if before.Player.SocialFlags&tradeCardMask != 0 {
		return nil
	}
	submitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = session.ExecuteExpected(submitCtx, before.Revision, aigame.Action{Kind: aigame.ActionSocialSetting, Command: "trade-card", Value: 1})
	cancel()
	if err != nil {
		return err
	}
	_, err = socialTwoPlayersLiveWait(ctx, session, func(snapshot aigame.Snapshot) bool {
		return snapshot.Phase == aigame.PhaseWorld && snapshot.Connected && snapshot.Player.SocialFlagsKnown && snapshot.Player.SocialFlags&tradeCardMask != 0 && snapshot.Revision > before.Revision
	})
	return err
}

func socialTwoPlayersLiveAdjacentRoute(ctx context.Context, navigator *ainavigation.Navigator, snapshot aigame.Snapshot) (ainavigation.Point, ainavigation.Route, error) {
	if navigator == nil {
		return ainavigation.Point{}, ainavigation.Route{}, errors.New("nil hometown navigator")
	}
	if snapshot.Position.Floor < 0 {
		return ainavigation.Point{}, ainavigation.Route{}, errors.New("hometown floor is unknown")
	}
	start := ainavigation.Point{X: int(snapshot.Position.X), Y: int(snapshot.Position.Y)}
	for _, delta := range []ainavigation.Point{{X: 1}, {Y: 1}, {X: -1}, {Y: -1}} {
		target := ainavigation.Point{X: start.X + delta.X, Y: start.Y + delta.Y}
		occupied := false
		for _, actor := range snapshot.Actors {
			if actor.X == int32(target.X) && actor.Y == int32(target.Y) {
				occupied = true
				break
			}
		}
		if occupied {
			continue
		}
		route, err := navigator.RouteContext(ctx, int(snapshot.Position.Floor), start, target)
		if err == nil && len(route.Directions) == 1 && len(route.Points) == 1 && route.Points[0] == target {
			return target, route, nil
		}
	}
	return ainavigation.Point{}, ainavigation.Route{}, fmt.Errorf("no unoccupied one-step route from %s", start)
}

func socialTwoPlayersLiveRefreshAB(ctx context.Context, session aiprovision.HeadlessSession, expectedName string) (aigame.Snapshot, error) {
	if session == nil {
		return aigame.Snapshot{}, errors.New("nil game session")
	}
	before, err := session.Observe(ctx)
	if err != nil {
		return aigame.Snapshot{}, err
	}
	if err := socialTwoPlayersLiveSubmitWithSnapshot(ctx, session, before, aigame.Mailbox()); err != nil {
		return aigame.Snapshot{}, err
	}
	return socialTwoPlayersLiveWait(ctx, session, func(snapshot aigame.Snapshot) bool {
		if !snapshot.AddressBookKnown || snapshot.AddressBookRevision <= before.AddressBookRevision {
			return false
		}
		_, err := socialTwoPlayersLiveAddressIndex(snapshot, expectedName)
		return err == nil
	})
}

func socialTwoPlayersLiveSubmitWithSnapshot(ctx context.Context, session aiprovision.HeadlessSession, before aigame.Snapshot, action aigame.Action) error {
	submitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return session.ExecuteExpected(submitCtx, before.Revision, action)
}

func socialTwoPlayersLiveAddressIndex(snapshot aigame.Snapshot, name string) (int32, error) {
	var index int32 = -1
	for _, entry := range snapshot.AddressBook {
		if !entry.Use || entry.Name != name {
			continue
		}
		if index >= 0 {
			return -1, fmt.Errorf("address book contains duplicate contact %q", name)
		}
		index = entry.Index
	}
	if index < 0 {
		return -1, fmt.Errorf("address book does not contain contact %q", name)
	}
	return index, nil
}

func socialTwoPlayersLiveWaitChat(ctx context.Context, session aiprovision.HeadlessSession, channel, token string, contains bool) (aigame.Snapshot, error) {
	return socialTwoPlayersLiveWait(ctx, session, func(snapshot aigame.Snapshot) bool {
		matches := 0
		for _, message := range snapshot.Chat {
			if message.Channel != channel {
				continue
			}
			if (contains && strings.Contains(message.Text, token)) || (!contains && message.Text == token) {
				matches++
			}
		}
		return matches == 1
	})
}
