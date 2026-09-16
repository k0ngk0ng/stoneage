// Package aiprovision owns the server-side identity boundary for persistent
// StoneAge AI players. It creates dedicated game accounts/characters and
// opens only the immutable account+slot bound to an airuntime profile.
//
// Game passwords are kept in a private filesystem store. They are never part
// of an airuntime.Profile, a Binding, a SessionLease, or a model prompt.
package aiprovision

import (
	"context"
	"errors"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
	"github.com/k0ngk0ng/stoneage/internal/auth"
)

var (
	ErrInvalidConfig        = errors.New("aiprovision: invalid configuration")
	ErrClosed               = errors.New("aiprovision: provider is closed")
	ErrAlreadyOpen          = errors.New("aiprovision: profile already has a game session")
	ErrBindingNotFound      = errors.New("aiprovision: profile game binding not found")
	ErrBindingConflict      = errors.New("aiprovision: immutable profile game binding conflict")
	ErrAccountUnavailable   = errors.New("aiprovision: game account is unavailable")
	ErrSecretUnavailable    = errors.New("aiprovision: game credential is unavailable")
	ErrCharacterUnavailable = errors.New("aiprovision: bound character is unavailable")
	ErrGameSession          = errors.New("aiprovision: game session is unavailable")
)

// Binding is the durable identity selected by an administrator. AccountID is
// the auth database identifier; AccountUsername is the legacy game login
// name. CharacterID is a stable server-side reference in the form
// "<account-username>:<slot>" for accounts provisioned by this package.
// There is deliberately no password field.
type Binding struct {
	ProfileID       string    `json:"profile_id"`
	AccountID       int64     `json:"account_id"`
	AccountUsername string    `json:"account_username"`
	CharacterSlot   int       `json:"character_slot"`
	CharacterID     string    `json:"character_id"`
	CharacterName   string    `json:"character_name"`
	CreatedAt       time.Time `json:"created_at"`
}

// GameSession is the narrow game surface needed by aiservice and by the
// provider's returned lease. The concrete implementation is aigame.Session.
type GameSession interface {
	Observe(context.Context) (aigame.Snapshot, error)
	ExecuteExpected(context.Context, uint64, aigame.Action) error
}

// HeadlessSession includes the authenticated character lifecycle used while
// provisioning and opening a profile. It is intentionally an interface so
// tests and service adapters can supply a fake without exposing credentials.
type HeadlessSession interface {
	GameSession
	Characters() []aigame.Character
	RefreshCharacters(context.Context) ([]aigame.Character, error)
	CreateCharacter(context.Context, aigame.CharacterCreate) error
	EnterCharacter(context.Context, string) error
	Events() <-chan aigame.Event
	Close() error
}

// SessionConnector is the only seam that receives game credentials. The
// production implementation is AigameConnector, which calls aigame.Login.
type SessionConnector interface {
	Login(context.Context, aigame.Config, aigame.Credentials) (HeadlessSession, error)
}

// SessionConnectorFunc adapts a login function to SessionConnector.
type SessionConnectorFunc func(context.Context, aigame.Config, aigame.Credentials) (HeadlessSession, error)

func (connector SessionConnectorFunc) Login(ctx context.Context, config aigame.Config, credentials aigame.Credentials) (HeadlessSession, error) {
	if connector == nil {
		return nil, ErrGameSession
	}
	return connector(ctx, config, credentials)
}

// AigameConnector is the production connector for the real named-protocol
// headless client.
type AigameConnector struct{}

func (AigameConnector) Login(ctx context.Context, config aigame.Config, credentials aigame.Credentials) (HeadlessSession, error) {
	return aigame.Login(ctx, config, credentials)
}

// SessionLease is the authenticated, character-scoped game capability handed
// to the service adapter. It contains no game password; the provider keeps
// that secret in its private store and clears its temporary copy before
// returning. Close revokes the provider lease and closes the session.
type SessionLease struct {
	Session HeadlessSession
	Binding Binding
	Wake    chan struct{}
	Close   func()
}

// ProviderConfig contains server-owned dependencies. Profiles and auth
// records are read from stores; no account password is accepted here.
type ProviderConfig struct {
	Auth       *auth.Store
	Secrets    *SecretStore
	Game       SessionConnector
	GameConfig aigame.Config
	WakeBuffer int
}

// Config is the provisioning configuration. ProviderConfig is embedded so a
// Provisioner can be used directly as a ProfileSessionProvider source.
type Config struct {
	ProviderConfig
	Profiles      *airuntime.Store
	AccountPrefix string
}
