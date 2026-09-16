// Package aifunding provisions the server-owned policy files consumed by the
// legacy GMSV funding capability.
//
// A policy is bound to both the byte-oriented game account and its save-file
// slot.  The package deliberately does not keep an authorization cache:
// removing a policy file therefore revokes the capability on the next C-side
// authorization check and on the next Allowed call.
package aifunding

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/k0ngk0ng/stoneage/internal/auth"
)

const (
	// PolicyDirEnv is the optional process environment override shared with
	// the legacy GMSV.  The value is configuration, never an account input.
	PolicyDirEnv = "STONEAGE_AI_FUNDING_POLICY_DIR"

	// DefaultPolicyDir matches STONEAGE_AF_DEFAULT_POLICY_DIR in the C
	// funding implementation.
	DefaultPolicyDir = "/run/stoneage/ai-funding/policies"

	policyVersion = 1
	// The C parser stores limits in a long and rejects values above this
	// bound.  Rejecting them while provisioning avoids publishing a policy
	// that GMSV would silently treat as ordinary-player behavior.
	maxPolicyLimit int64 = 1_000_000_000

	maxPolicyLine  = 768
	maxPolicyValue = 512
	maxPolicyKey   = 64
)

var (
	ErrInvalidAccount = errors.New("aifunding: invalid account")
	ErrInvalidSlot    = errors.New("aifunding: invalid slot")
	ErrInvalidLimit   = errors.New("aifunding: invalid limit")
	ErrInvalidDir     = errors.New("aifunding: invalid policy directory")
	ErrUnsafePolicy   = errors.New("aifunding: unsafe policy file")
)

// Policy is the server-side representation of one C funding policy.
// Provision always writes Enabled=true and NPCUnlimited=true; the fields are
// retained here so callers and tests can describe the exact file format.
type Policy struct {
	Account      string
	Slot         int
	Enabled      bool
	NPCUnlimited bool
	TradeLimit   int64
	BankLimit    int64
	DropLimit    int64
}

// Manager owns one fixed policy directory.  Policy files are intentionally
// file-backed because the legacy GMSV reads the same directory independently.
type Manager struct {
	mu  sync.RWMutex
	dir string
}

// NewManager creates or opens a private, fixed policy directory.  The final
// directory component may not be a symlink.  Parent aliases such as macOS's
// /var -> /private/var are retained because they are ordinary stable OS path
// aliases and callers may use the configured spelling when sharing the path
// with the legacy service.
func NewManager(dir string) (*Manager, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("%w: directory is empty", ErrInvalidDir)
	}
	abs, err := filepath.Abs(filepath.Clean(dir))
	if err != nil || abs == "" || abs == string(filepath.Separator) {
		return nil, fmt.Errorf("%w: dedicated directory is required", ErrInvalidDir)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, fmt.Errorf("%w: create directory: %v", ErrInvalidDir, err)
	}
	if err := validateDirectory(abs); err != nil {
		// A directory already created by the service may have inherited a
		// permissive mode. If it is a real directory, tighten it before using
		// it; later operations fail closed if another actor loosens it.
		info, statErr := os.Lstat(abs)
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, err
		}
		if chmodErr := os.Chmod(abs, 0o700); chmodErr != nil {
			return nil, err
		}
		if validateErr := validateDirectory(abs); validateErr != nil {
			return nil, validateErr
		}
	}

	// A policy is a capability grant. Keep its directory private even when a
	// pre-existing path was created with a permissive umask.
	if err := os.Chmod(abs, 0o700); err != nil {
		return nil, fmt.Errorf("%w: protect directory: %v", ErrInvalidDir, err)
	}
	return &Manager{dir: abs}, nil
}

// NewDefaultManager uses the same environment override and default path as
// the legacy GMSV implementation.
func NewDefaultManager() (*Manager, error) {
	dir := strings.TrimSpace(os.Getenv(PolicyDirEnv))
	if dir == "" {
		dir = DefaultPolicyDir
	}
	return NewManager(dir)
}

// Dir returns the fixed directory owned by m.
func (m *Manager) Dir() string {
	if m == nil {
		return ""
	}
	return m.dir
}

// PolicyPath returns the exact filename consumed by the C implementation:
// <policy-dir>/<account>.<slot>.policy.
func (m *Manager) PolicyPath(account string, slot int) (string, error) {
	if m == nil || m.dir == "" {
		return "", fmt.Errorf("%w: manager is not configured", ErrInvalidDir)
	}
	if err := validateAccount(account); err != nil {
		return "", err
	}
	if err := validateSlot(slot); err != nil {
		return "", err
	}
	return filepath.Join(m.dir, fmt.Sprintf("%s.%d.policy", account, slot)), nil
}

// Provision atomically publishes an enabled, NPC-unlimited policy.  The
// external limit is applied independently to trade, bank and drop transfers,
// matching the current Profile.ExternalSpendLimit contract.
func (m *Manager) Provision(account string, slot int, externalLimit int64) error {
	path, err := m.PolicyPath(account, slot)
	if err != nil {
		return err
	}
	if externalLimit < 0 || externalLimit > maxPolicyLimit {
		return fmt.Errorf("%w: must be between 0 and %d", ErrInvalidLimit, maxPolicyLimit)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkDirectory(); err != nil {
		return err
	}
	if err := validateExistingTarget(path); err != nil {
		return err
	}

	policy := Policy{Account: account, Slot: slot, Enabled: true, NPCUnlimited: true,
		TradeLimit: externalLimit, BankLimit: externalLimit, DropLimit: externalLimit}
	data := []byte(formatPolicy(policy))
	temporary, err := os.CreateTemp(m.dir, ".stoneage-ai-funding-*.tmp")
	if err != nil {
		return fmt.Errorf("aifunding: create temporary policy: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("aifunding: protect temporary policy: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("aifunding: write policy: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("aifunding: sync policy: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("aifunding: close policy: %w", err)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("aifunding: publish policy: %w", err)
	}
	if err := syncDirectory(m.dir); err != nil {
		return fmt.Errorf("aifunding: sync policy directory: %w", err)
	}
	return nil
}

// Revoke atomically removes one policy.  It is idempotent so cleanup paths
// can revoke a capability even if a prior request already removed it.
func (m *Manager) Revoke(account string, slot int) error {
	path, err := m.PolicyPath(account, slot)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkDirectory(); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("aifunding: revoke policy: %w", err)
	}
	if err := syncDirectory(m.dir); err != nil {
		return fmt.Errorf("aifunding: sync policy directory: %w", err)
	}
	return nil
}

// Allowed is the trusted authorization source for automation.UnlimitedFunds.
// It reads and validates the policy on every call, so deletion or replacement
// takes effect immediately without a cache flush.
func (m *Manager) Allowed(account string, slot int) bool {
	path, err := m.PolicyPath(account, slot)
	if err != nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if err := m.checkDirectory(); err != nil {
		return false
	}
	return loadPolicy(path, account, slot) == nil
}

func validateAccount(account string) error {
	if err := auth.ValidateGameUsername([]byte(account)); err != nil {
		return fmt.Errorf("%w: %q", ErrInvalidAccount, account)
	}
	return nil
}

func validateSlot(slot int) error {
	if slot != 0 && slot != 1 {
		return fmt.Errorf("%w: %d", ErrInvalidSlot, slot)
	}
	return nil
}

func validateDirectory(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("%w: inspect directory: %v", ErrInvalidDir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: directory must be a real directory", ErrInvalidDir)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%w: directory must be private", ErrInvalidDir)
	}
	return nil
}

func (m *Manager) checkDirectory() error {
	if m == nil || m.dir == "" {
		return fmt.Errorf("%w: manager is not configured", ErrInvalidDir)
	}
	return validateDirectory(m.dir)
}

func validateExistingTarget(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: inspect target: %v", ErrUnsafePolicy, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: target must be a regular file", ErrUnsafePolicy)
	}
	return nil
}

func formatPolicy(policy Policy) string {
	return fmt.Sprintf("version=%d\naccount=%s\nslot=%d\nnpc_spend=unlimited\nenabled=%d\ntrade_limit=%d\nbank_limit=%d\ndrop_limit=%d\n",
		policyVersion, policy.Account, policy.Slot, boolInt(policy.NPCUnlimited && policy.Enabled),
		policy.TradeLimit, policy.BankLimit, policy.DropLimit)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

type parsedPolicy struct {
	account      string
	slot         int
	enabled      bool
	npcUnlimited bool
}

const (
	seenVersion = 1 << iota
	seenAccount
	seenSlot
	seenNPC
	seenEnabled
	seenTrade
	seenBank
	seenDrop
)

// loadPolicy follows the C parser's required fields and active-policy rules.
// Limits are parsed too, even though Allowed only needs the capability bit,
// so a malformed or overlarge policy cannot become an accidental grant.
func loadPolicy(path, account string, slot int) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return ErrUnsafePolicy
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	// Check the opened descriptor as well as the path. This closes the normal
	// replacement race without ever accepting a symlink as a policy file.
	openedInfo, err := file.Stat()
	if err != nil || openedInfo.Mode()&os.ModeSymlink != 0 || !openedInfo.Mode().IsRegular() || openedInfo.Mode().Perm()&0o077 != 0 {
		return ErrUnsafePolicy
	}

	parsed := parsedPolicy{enabled: true}
	seen := 0
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, maxPolicyLine), maxPolicyLine)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.IndexByte(line, 0) >= 0 {
			return ErrUnsafePolicy
		}
		line = trimPolicyLine(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		equals := strings.IndexByte(line, '=')
		if equals < 0 {
			return ErrUnsafePolicy
		}
		key := trimPolicySpace(line[:equals])
		value := trimPolicySpace(line[equals+1:])
		if !validPolicyKey(key) || len(value) >= maxPolicyValue {
			return ErrUnsafePolicy
		}
		switch key {
		case "version":
			if seen&seenVersion != 0 || parsePolicyLong(value) != policyVersion {
				return ErrUnsafePolicy
			}
			seen |= seenVersion
		case "account":
			if seen&seenAccount != 0 || value == "" || len(value) >= 256 {
				return ErrUnsafePolicy
			}
			parsed.account = value
			seen |= seenAccount
		case "slot", "character_slot":
			if seen&seenSlot != 0 {
				return ErrUnsafePolicy
			}
			parsed.slot = int(parsePolicyLong(value))
			if parsed.slot < 0 || parsed.slot > 1 || parsePolicyLong(value) < 0 {
				return ErrUnsafePolicy
			}
			seen |= seenSlot
		case "npc_spend":
			if seen&seenNPC != 0 || value != "unlimited" {
				return ErrUnsafePolicy
			}
			parsed.npcUnlimited = true
			seen |= seenNPC
		case "enabled":
			valueInt := parsePolicyLong(value)
			if seen&seenEnabled != 0 || valueInt < 0 || valueInt > 1 {
				return ErrUnsafePolicy
			}
			parsed.enabled = valueInt != 0
			seen |= seenEnabled
		case "trade_limit":
			if seen&seenTrade != 0 || parsePolicyLong(value) < 0 {
				return ErrUnsafePolicy
			}
			seen |= seenTrade
		case "bank_limit":
			if seen&seenBank != 0 || parsePolicyLong(value) < 0 {
				return ErrUnsafePolicy
			}
			seen |= seenBank
		case "drop_limit":
			if seen&seenDrop != 0 || parsePolicyLong(value) < 0 {
				return ErrUnsafePolicy
			}
			seen |= seenDrop
		default:
			// The C parser permits forward-compatible metadata after key
			// validation, so unknown valid keys are intentionally ignored.
		}
	}
	if err := scanner.Err(); err != nil {
		return ErrUnsafePolicy
	}
	if seen&(seenVersion|seenAccount|seenSlot|seenNPC) != (seenVersion|seenAccount|seenSlot|seenNPC) ||
		!parsed.enabled || !parsed.npcUnlimited || parsed.account != account || parsed.slot != slot {
		return ErrUnsafePolicy
	}
	return nil
}

func trimPolicySpace(value string) string {
	return strings.Trim(value, " \t\r\n")
}

func trimPolicyLine(value string) string {
	// Scanner already removes LF. C also trims CR at the right edge and only
	// spaces/tabs from the left edge.
	value = strings.TrimLeft(value, " \t")
	return strings.TrimRight(value, " \t\r\n")
}

func validPolicyKey(key string) bool {
	if key == "" || len(key) >= maxPolicyKey {
		return false
	}
	for _, character := range []byte(key) {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '_' || character == '-' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func parsePolicyLong(value string) int64 {
	if value == "" {
		return -1
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 || parsed > maxPolicyLimit {
		return -1
	}
	return parsed
}

func syncDirectory(dir string) error {
	file, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Sync(); err != nil {
		// Some filesystems/platforms do not support syncing directory handles.
		// Linux, where the legacy C service runs, does; return the error there.
		return err
	}
	return nil
}
