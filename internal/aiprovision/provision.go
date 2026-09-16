package aiprovision

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiinitial"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
	"github.com/k0ngk0ng/stoneage/internal/auth"
)

var profileIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

const (
	defaultAccountPrefix  = "ai"
	maxCharacterNameBytes = 64
	maxAccountPrefixBytes = 5
	provisionAttempts     = 16
)

// CreateRequest describes the non-secret part of an AI profile. Account and
// character identity fields in Profile are ignored and replaced by the newly
// created dedicated identity. This prevents an admin form or stale profile
// JSON from selecting another account's password.
type CreateRequest struct {
	InitialState *aiinitial.Request
	// Initializer is supplied by server wiring, never a browser or model.
	Initializer     Initializer
	ProfileID       string
	AccountUsername string
	CharacterSlot   int
	CharacterName   string
	CharacterCreate aigame.CharacterCreate
	// UnlimitedFunds is a server-side capability for this AI player. A nil
	// value keeps the AI default: the player can pay every in-game NPC cost
	// without depending on the visible gold balance. A non-nil value is an
	// explicit administrative choice.
	UnlimitedFunds *bool

	Profile airuntime.Profile
	Actor   string
	ActorID *int64
}

// Provisioned is returned after the account, character and profile binding
// have been persisted. It contains safe account metadata only.
type Provisioned struct {
	Profile airuntime.Profile
	Account auth.Account
	Binding Binding
}

// Provisioner creates AI-only game identities and their airuntime profiles.
// It also exposes Provider for later supervisor Factory.Open calls.
type Provisioner struct {
	config   Config
	provider *ProfileSessionProvider
}

// New validates the provisioning dependencies and creates the immutable
// binding schema in the auth database.
func New(config Config) (*Provisioner, error) {
	if config.Auth == nil || config.Profiles == nil || config.Secrets == nil || config.Game == nil {
		return nil, fmt.Errorf("%w: auth, profiles, secrets and game are required", ErrInvalidConfig)
	}
	if err := ensureBindingSchema(context.Background(), config.Auth.DB()); err != nil {
		return nil, err
	}
	if config.AccountPrefix == "" {
		config.AccountPrefix = defaultAccountPrefix
	}
	config.AccountPrefix = auth.CanonicalGameUsername(config.AccountPrefix)
	if len(config.AccountPrefix) == 0 || len(config.AccountPrefix) > maxAccountPrefixBytes {
		return nil, fmt.Errorf("%w: account prefix is too long", ErrInvalidConfig)
	}
	if err := auth.ValidateGameUsername([]byte(config.AccountPrefix + "_" + "000000000")); err != nil {
		return nil, fmt.Errorf("%w: account prefix is invalid", ErrInvalidConfig)
	}
	provider, err := NewProfileSessionProvider(config.ProviderConfig)
	if err != nil {
		return nil, err
	}
	return &Provisioner{config: config, provider: provider}, nil
}

// NewProvisioner is a descriptive alias for New.
func NewProvisioner(config Config) (*Provisioner, error) { return New(config) }

// Provider returns the profile session provider backed by this provisioner.
func (provisioner *Provisioner) Provider() *ProfileSessionProvider {
	if provisioner == nil {
		return nil
	}
	return provisioner.provider
}

// Binding returns the immutable identity associated with a profile.
func (provisioner *Provisioner) Binding(ctx context.Context, profileID string) (Binding, error) {
	if provisioner == nil || provisioner.config.Auth == nil {
		return Binding{}, ErrInvalidConfig
	}
	return getBinding(ctx, provisioner.config.Auth.DB(), profileID)
}

// CreateAI creates a fresh auth account, creates and verifies one character
// in the requested slot, then stores a profile whose account+character IDs
// are immutable through the binding table. The temporary login session is
// always closed before this method returns.
func (provisioner *Provisioner) CreateAI(ctx context.Context, request CreateRequest) (Provisioned, error) {
	if provisioner == nil {
		return Provisioned{}, ErrInvalidConfig
	}
	if ctx == nil {
		ctx = context.Background()
	}
	release, err := provisioner.lockInitialPublication()
	if err != nil {
		return Provisioned{}, err
	}
	defer release()
	if err := validateCreateRequest(request); err != nil {
		return Provisioned{}, err
	}
	profileID := strings.TrimSpace(request.ProfileID)
	if profileID == "" {
		profileID = strings.TrimSpace(request.Profile.ID)
	}
	if !profileIDPattern.MatchString(profileID) {
		return Provisioned{}, fmt.Errorf("%w: profile ID is invalid", ErrInvalidConfig)
	}
	if _, err := provisioner.config.Profiles.GetProfile(ctx, profileID); err == nil {
		return Provisioned{}, ErrBindingConflict
	} else if !errors.Is(err, airuntime.ErrNotFound) {
		return Provisioned{}, fmt.Errorf("%w: check profile: %v", ErrInvalidConfig, err)
	}

	username := strings.TrimSpace(request.AccountUsername)
	if username != "" {
		username = auth.CanonicalGameUsername(username)
		if err := auth.ValidateGameUsername([]byte(username)); err != nil {
			return Provisioned{}, fmt.Errorf("%w: account username is invalid", ErrInvalidConfig)
		}
	}
	characterName := strings.TrimSpace(request.CharacterName)
	if characterName == "" {
		characterName = strings.TrimSpace(request.Profile.Character.Name)
	}
	if err := validateCharacterName(characterName); err != nil {
		return Provisioned{}, err
	}

	initial, err := provisioner.reserveInitial(ctx, profileID, request.InitialState, request.Initializer)
	if err != nil {
		return Provisioned{}, err
	}
	initialPublished := false
	var initialAccountID int64
	defer func() {
		if initial == nil || initialPublished {
			return
		}
		persist, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		// Account insertion and journal association commit together, including
		// when the caller loses the commit response.
		if initialAccountID == 0 {
			var linked sql.NullInt64
			if err := provisioner.config.Auth.DB().QueryRowContext(persist, "SELECT account_id FROM ai_initial_states WHERE profile_id=?", profileID).Scan(&linked); err == nil && linked.Valid {
				initialAccountID = linked.Int64
			}
		}
		// An ambiguous native mutation is never automatically retried.
		if initial.Actual != nil && initial.PendingProfile != nil {
			initial.Status = "publication_pending"
		} else {
			initial.Status = "failed_or_unconfirmed"
		}
		if initialAccountID != 0 {
			_ = provisioner.config.Auth.SetAccountStatusAs(persist, request.ActorID, initialAccountID, auth.AccountDisabled)
		}
		_ = provisioner.saveInitial(persist, profileID, initialAccountID, initial)
	}()
	var account auth.Account
	var password []byte
	for attempt := 0; attempt < provisionAttempts; attempt++ {
		candidate := username
		if candidate == "" {
			candidate, err = provisioner.generatedUsername()
			if err != nil {
				return Provisioned{}, err
			}
		}
		passwordText, passwordErr := randomString(auth.MaxGamePasswordBytes, passwordAlphabet)
		if passwordErr != nil {
			return Provisioned{}, fmt.Errorf("%w: generate account credential", ErrSecretUnavailable)
		}
		var link func(*sql.Tx, int64) error
		if initial != nil {
			link = func(tx *sql.Tx, accountID int64) error {
				result, err := tx.ExecContext(ctx, "UPDATE ai_initial_states SET account_id=? WHERE profile_id=? AND account_id IS NULL", accountID, profileID)
				if err != nil {
					return err
				}
				changed, err := result.RowsAffected()
				if err != nil {
					return err
				}
				if changed != 1 {
					return ErrBindingConflict
				}
				return nil
			}
		}
		candidateAccount, createErr := provisioner.config.Auth.CreateAccountLinkedAs(ctx, request.ActorID, candidate, []byte(passwordText), link)
		if createErr == nil {
			account = candidateAccount
			password = []byte(passwordText)
			break
		}
		if username != "" || !strings.Contains(strings.ToLower(createErr.Error()), "already exists") {
			return Provisioned{}, fmt.Errorf("%w: create account", ErrAccountUnavailable)
		}
	}
	if account.ID == 0 || len(password) == 0 {
		return Provisioned{}, fmt.Errorf("%w: generate a unique account", ErrAccountUnavailable)
	}
	defer zeroBytes(password)
	initialAccountID = account.ID
	if initial != nil {
		initial.Status = "creating"
		if err := provisioner.saveInitial(ctx, profileID, account.ID, initial); err != nil {
			return Provisioned{}, err
		}
	}
	if err := provisioner.config.Secrets.Put(account.ID, password); err != nil {
		_ = provisioner.config.Auth.SetAccountStatusAs(context.Background(), request.ActorID, account.ID, auth.AccountDisabled)
		return Provisioned{}, err
	}
	credentialSaved := false
	defer func() {
		if credentialSaved || (initial != nil && initial.Actual != nil && initial.PendingProfile != nil) {
			return
		}
		_ = provisioner.config.Secrets.Delete(account.ID)
	}()

	gameSession, err := provisioner.config.Game.Login(ctx, provisioner.config.GameConfig, aigame.Credentials{Account: account.Username, PasswordBytes: password})
	if err != nil || gameSession == nil {
		_ = provisioner.config.Auth.SetAccountStatusAs(context.Background(), request.ActorID, account.ID, auth.AccountDisabled)
		return Provisioned{}, fmt.Errorf("%w: login dedicated account", ErrGameSession)
	}
	gameClosed := false
	defer func() {
		if !gameClosed {
			_ = gameSession.Close()
		}
	}()

	create := request.CharacterCreate
	create.DataPlace = int32(request.CharacterSlot)
	create.Name = characterName
	if initial != nil {
		create.Hometown = int32(initial.Resolved.Hometown)
	}
	if err := gameSession.CreateCharacter(ctx, create); err != nil {
		_ = provisioner.config.Auth.SetAccountStatusAs(context.Background(), request.ActorID, account.ID, auth.AccountDisabled)
		return Provisioned{}, fmt.Errorf("%w: create dedicated character", ErrCharacterUnavailable)
	}
	characters, err := gameSession.RefreshCharacters(ctx)
	if err != nil {
		_ = provisioner.config.Auth.SetAccountStatusAs(context.Background(), request.ActorID, account.ID, auth.AccountDisabled)
		return Provisioned{}, fmt.Errorf("%w: refresh dedicated characters", ErrCharacterUnavailable)
	}
	selected, ok := findCharacter(characters, request.CharacterSlot, characterName)
	if !ok {
		_ = provisioner.config.Auth.SetAccountStatusAs(context.Background(), request.ActorID, account.ID, auth.AccountDisabled)
		return Provisioned{}, fmt.Errorf("%w: created character was not returned in requested slot", ErrCharacterUnavailable)
	}
	if err := gameSession.EnterCharacter(ctx, selected.Name); err != nil {
		_ = provisioner.config.Auth.SetAccountStatusAs(context.Background(), request.ActorID, account.ID, auth.AccountDisabled)
		return Provisioned{}, fmt.Errorf("%w: enter dedicated character", ErrCharacterUnavailable)
	}

	accountID := account.Username
	characterID := fmt.Sprintf("%s:%d", account.Username, request.CharacterSlot)
	binding := Binding{ProfileID: profileID, AccountID: account.ID, AccountUsername: account.Username,
		CharacterSlot: request.CharacterSlot, CharacterID: characterID, CharacterName: selected.Name, CreatedAt: time.Now().UTC()}
	profile := request.Profile
	profile.ID = profileID
	profile.Account = airuntime.AccountIdentity{ID: accountID, Username: account.Username}
	profile.Character = airuntime.CharacterIdentity{ID: characterID, Name: selected.Name}
	if profile.Status == "" {
		profile.Status = airuntime.ProfileStatusStopped
	}
	// AI players default to the server-enforced unlimited-funding capability.
	// The pointer distinguishes that default from an explicit administrative
	// false; a plain bool in Profile cannot carry that distinction.
	profile.UnlimitedFunds = true
	if request.UnlimitedFunds != nil {
		profile.UnlimitedFunds = *request.UnlimitedFunds
	}
	if initial != nil {
		initial.PendingProfile = &profile
		initial.Binding = &binding
		initial.Status = "applying"
		if err := provisioner.saveInitial(ctx, profileID, account.ID, initial); err != nil {
			return Provisioned{}, err
		}
		actual, err := request.Initializer.Apply(ctx, account.Username, request.CharacterSlot, initial.Resolved)
		if err != nil {
			return Provisioned{}, fmt.Errorf("%w: initialize dedicated character", ErrCharacterUnavailable)
		}
		if !aigame.ValidPersistentCharacterID(actual.PersistentCharacterID) {
			return Provisioned{}, fmt.Errorf("%w: initialized character has no persisted identity", ErrInitialUnconfirmed)
		}
		initial.Actual = &actual
		initial.Status = "applied"
		if err := provisioner.saveInitial(ctx, profileID, account.ID, initial); err != nil {
			return Provisioned{}, err
		}
	}

	if err := insertBinding(ctx, provisioner.config.Auth.DB(), binding); err != nil {
		_ = provisioner.config.Auth.SetAccountStatusAs(context.Background(), request.ActorID, account.ID, auth.AccountDisabled)
		return Provisioned{}, err
	}
	created, err := provisioner.config.Profiles.CreateProfileAs(ctx, profile, request.Actor)
	if err != nil {
		_ = provisioner.config.Auth.SetAccountStatusAs(context.Background(), request.ActorID, account.ID, auth.AccountDisabled)
		return Provisioned{}, fmt.Errorf("%w: create AI profile", ErrBindingConflict)
	}
	// Release the creation session before the publication marker lets the
	// runtime open this character through a fresh lease.
	gameClosed = true
	_ = gameSession.Close()
	if initial != nil {
		initial.Status = "published"
		if err := provisioner.saveInitial(ctx, profileID, account.ID, initial); err != nil {
			return Provisioned{}, err
		}
	}
	initialPublished = true
	credentialSaved = true
	return Provisioned{Profile: created, Account: account, Binding: binding}, nil
}

// Provision is a concise alias for CreateAI.
func (provisioner *Provisioner) Provision(ctx context.Context, request CreateRequest) (Provisioned, error) {
	return provisioner.CreateAI(ctx, request)
}

func (provisioner *Provisioner) generatedUsername() (string, error) {
	suffix, err := randomString(9, usernameAlphabet)
	if err != nil {
		return "", fmt.Errorf("%w: generate account username", ErrAccountUnavailable)
	}
	return auth.CanonicalGameUsername(provisioner.config.AccountPrefix + "_" + suffix), nil
}

func validateCreateRequest(request CreateRequest) error {
	if request.CharacterSlot < 0 || request.CharacterSlot > 1 {
		return fmt.Errorf("%w: character slot must be 0 or 1", ErrInvalidConfig)
	}
	if request.ProfileID != "" && !profileIDPattern.MatchString(strings.TrimSpace(request.ProfileID)) {
		return fmt.Errorf("%w: profile ID is invalid", ErrInvalidConfig)
	}
	if request.ProfileID != "" && request.Profile.ID != "" && strings.TrimSpace(request.ProfileID) != strings.TrimSpace(request.Profile.ID) {
		return fmt.Errorf("%w: profile IDs disagree", ErrInvalidConfig)
	}
	return nil
}

func validateCharacterName(name string) error {
	if name == "" || len([]byte(name)) > maxCharacterNameBytes || strings.ContainsAny(name, "\x00\r\n") {
		return fmt.Errorf("%w: character name is invalid", ErrInvalidConfig)
	}
	return nil
}

func findCharacter(characters []aigame.Character, slot int, name string) (aigame.Character, bool) {
	for _, character := range characters {
		if character.Slot == slot && character.Name == name {
			return character, true
		}
	}
	return aigame.Character{}, false
}
