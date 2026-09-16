package aiprovision

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
	"github.com/k0ngk0ng/stoneage/internal/auth"
)

const defaultWakeBuffer = 256

// ProfileSessionProvider resolves an airuntime profile to its immutable game
// account+slot binding, logs into that account with a server-owned secret and
// enters exactly the bound character. One profile can have at most one live
// lease at a time.
type ProfileSessionProvider struct {
	config ProviderConfig

	mu     sync.Mutex
	closed bool
	active map[string]*providerLease
}

// providerLease is reserved before any network work starts. The reservation
// closes the duplicate-open race and lets Close cancel an Open which is still
// waiting for the game gateway.
type providerLease struct {
	mu     sync.Mutex
	closed bool
	cancel context.CancelFunc
	close  func()
}

func (lease *providerLease) setCancel(cancel context.CancelFunc) bool {
	if lease == nil {
		if cancel != nil {
			cancel()
		}
		return false
	}
	lease.mu.Lock()
	if lease.closed {
		lease.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		return false
	}
	lease.cancel = cancel
	lease.mu.Unlock()
	return true
}

func (lease *providerLease) setClose(callback func()) bool {
	if lease == nil {
		if callback != nil {
			callback()
		}
		return false
	}
	lease.mu.Lock()
	if lease.closed {
		lease.mu.Unlock()
		if callback != nil {
			callback()
		}
		return false
	}
	lease.close = callback
	lease.mu.Unlock()
	return true
}

func (lease *providerLease) closeNow() {
	if lease == nil {
		return
	}
	lease.mu.Lock()
	if lease.closed {
		lease.mu.Unlock()
		return
	}
	lease.closed = true
	cancel := lease.cancel
	callback := lease.close
	lease.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if callback != nil {
		callback()
	}
}

func (lease *providerLease) isClosed() bool {
	if lease == nil {
		return true
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	return lease.closed
}

// NewProfileSessionProvider validates the server-owned dependencies and
// prepares the immutable binding schema. It does not open a game connection.
func NewProfileSessionProvider(config ProviderConfig) (*ProfileSessionProvider, error) {
	if config.Auth == nil || config.Secrets == nil || config.Game == nil {
		return nil, fmt.Errorf("%w: auth, secrets and game are required", ErrInvalidConfig)
	}
	if err := ensureBindingSchema(context.Background(), config.Auth.DB()); err != nil {
		return nil, err
	}
	if config.WakeBuffer <= 0 {
		config.WakeBuffer = defaultWakeBuffer
	}
	if config.WakeBuffer > 1<<20 {
		return nil, fmt.Errorf("%w: wake buffer is too large", ErrInvalidConfig)
	}
	return &ProfileSessionProvider{config: config, active: make(map[string]*providerLease)}, nil
}

// Open authenticates one immutable profile binding and returns a
// character-scoped lease. Passwords are read only after the auth account has
// been confirmed active, passed to the connector, and wiped from the local
// buffer before this method returns. Connector and protocol details are
// intentionally reduced to package errors so a password cannot escape in an
// error string.
func (provider *ProfileSessionProvider) Open(ctx context.Context, profile airuntime.Profile) (SessionLease, error) {
	if provider == nil {
		return SessionLease{}, ErrInvalidConfig
	}
	if ctx == nil {
		ctx = context.Background()
	}
	profileID := strings.TrimSpace(profile.ID)
	if profile.ID != profileID || !profileIDPattern.MatchString(profileID) {
		return SessionLease{}, fmt.Errorf("%w: profile ID is invalid", ErrBindingConflict)
	}

	binding, err := getBinding(ctx, provider.config.Auth.DB(), profileID)
	if err != nil {
		return SessionLease{}, err
	}
	if err := validateProfileBinding(profile, binding); err != nil {
		return SessionLease{}, err
	}

	if err := checkInitialPublication(ctx, provider.config.Auth.DB(), profileID); err != nil {
		return SessionLease{}, err
	}
	reservation := &providerLease{}
	provider.mu.Lock()
	if provider.closed {
		provider.mu.Unlock()
		return SessionLease{}, ErrClosed
	}
	if provider.active[profileID] != nil {
		provider.mu.Unlock()
		return SessionLease{}, ErrAlreadyOpen
	}
	provider.active[profileID] = reservation
	provider.mu.Unlock()

	openCtx, cancel := context.WithCancel(ctx)
	reservation.setCancel(cancel)

	var sessionMu sync.Mutex
	var gameSession HeadlessSession
	cleaned := false
	stopEvents := make(chan struct{})
	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			// Mark the reservation closed before taking the session pointer. If
			// a gateway login completes concurrently, adoptSession will close
			// that late result instead of leaking a socket.
			sessionMu.Lock()
			cleaned = true
			live := gameSession
			sessionMu.Unlock()
			cancel()
			close(stopEvents)
			if live != nil {
				_ = live.Close()
			}
			provider.release(profileID, reservation)
		})
	}
	if !reservation.setClose(cleanup) {
		return SessionLease{}, ErrClosed
	}
	failed := true
	defer func() {
		if failed {
			cleanup()
		}
	}()

	adoptSession := func(candidate HeadlessSession) bool {
		sessionMu.Lock()
		if cleaned {
			sessionMu.Unlock()
			if candidate != nil {
				_ = candidate.Close()
			}
			return false
		}
		gameSession = candidate
		sessionMu.Unlock()
		return true
	}

	account, err := provider.config.Auth.GetAccount(openCtx, binding.AccountID)
	if err != nil || account.Username != binding.AccountUsername || account.Status != auth.AccountActive {
		return SessionLease{}, ErrAccountUnavailable
	}
	password, err := provider.config.Secrets.read(binding.AccountID)
	if err != nil {
		return SessionLease{}, ErrSecretUnavailable
	}
	// No path below this point may retain the password. Aigame copies the
	// credential bytes while constructing the login packet; adapters must do
	// the same and never put them into a lease or profile.
	session, loginErr := provider.config.Game.Login(openCtx, provider.config.GameConfig,
		aigame.Credentials{Account: account.Username, PasswordBytes: password})
	zeroBytes(password)
	if loginErr != nil || session == nil {
		if session != nil {
			_ = session.Close()
		}
		return SessionLease{}, ErrGameSession
	}
	if !adoptSession(session) {
		return SessionLease{}, ErrClosed
	}

	characters, err := session.RefreshCharacters(openCtx)
	if err != nil {
		return SessionLease{}, ErrCharacterUnavailable
	}
	selected, ok := findCharacter(characters, binding.CharacterSlot, binding.CharacterName)
	if !ok || selected.Name != binding.CharacterName {
		return SessionLease{}, ErrCharacterUnavailable
	}
	if err := session.EnterCharacter(openCtx, selected.Name); err != nil {
		return SessionLease{}, ErrCharacterUnavailable
	}

	wake := make(chan struct{}, provider.config.WakeBuffer)
	if events := session.Events(); events != nil {
		go provider.forwardEvents(events, wake, stopEvents, cleanup)
	}
	if reservation.isClosed() {
		return SessionLease{}, ErrClosed
	}
	failed = false
	return SessionLease{Session: session, Binding: binding, Wake: wake, Close: cleanup}, nil
}

func validateProfileBinding(profile airuntime.Profile, binding Binding) error {
	if binding.ProfileID != profile.ID ||
		profile.Account.ID != binding.AccountUsername ||
		profile.Account.Username != binding.AccountUsername ||
		profile.Character.ID != binding.CharacterID ||
		profile.Character.Name != binding.CharacterName {
		return ErrBindingConflict
	}
	return nil
}

func (provider *ProfileSessionProvider) forwardEvents(events <-chan aigame.Event, wake chan<- struct{}, stop <-chan struct{}, cleanup func()) {
	for {
		select {
		case <-stop:
			return
		case _, ok := <-events:
			if !ok {
				cleanup()
				return
			}
			select {
			case wake <- struct{}{}:
			default:
			}
		}
	}
}

func (provider *ProfileSessionProvider) release(profileID string, reservation *providerLease) {
	provider.mu.Lock()
	if provider.active[profileID] == reservation {
		delete(provider.active, profileID)
	}
	provider.mu.Unlock()
}

// Close stops all active and in-flight leases. It is safe to call repeatedly.
func (provider *ProfileSessionProvider) Close() error {
	if provider == nil {
		return nil
	}
	provider.mu.Lock()
	if provider.closed {
		provider.mu.Unlock()
		return nil
	}
	provider.closed = true
	leases := make([]*providerLease, 0, len(provider.active))
	for _, lease := range provider.active {
		leases = append(leases, lease)
	}
	provider.mu.Unlock()
	for _, lease := range leases {
		lease.closeNow()
	}
	return nil
}

var _ interface {
	Open(context.Context, airuntime.Profile) (SessionLease, error)
	Close() error
} = (*ProfileSessionProvider)(nil)
