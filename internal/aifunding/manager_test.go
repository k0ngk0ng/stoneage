package aifunding

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testManager(t *testing.T) *Manager {
	t.Helper()
	manager, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return manager
}

func TestProvisionAllowedRevokeAndSlotIsolation(t *testing.T) {
	manager := testManager(t)
	account := "Ai_User-1"
	path, err := manager.PolicyPath(account, 1)
	if err != nil {
		t.Fatalf("PolicyPath: %v", err)
	}
	wantPath := filepath.Join(manager.Dir(), account+".1.policy")
	if path != wantPath {
		t.Fatalf("policy path = %q, want %q", path, wantPath)
	}
	if manager.Allowed(account, 1) {
		t.Fatal("unprovisioned account was allowed")
	}
	if err := manager.Provision(account, 1, 500); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if !manager.Allowed(account, 1) {
		t.Fatal("provisioned account was not allowed")
	}
	if manager.Allowed(account, 0) {
		t.Fatal("policy leaked to another save slot")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read policy: %v", err)
	}
	want := "version=1\naccount=Ai_User-1\nslot=1\nnpc_spend=unlimited\nenabled=1\ntrade_limit=500\nbank_limit=500\ndrop_limit=500\n"
	if string(data) != want {
		t.Fatalf("policy contents = %q, want %q", data, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat policy: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("policy mode = %o, want 600", info.Mode().Perm())
	}
	if err := manager.Revoke(account, 1); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if manager.Allowed(account, 1) {
		t.Fatal("revoked account remained allowed")
	}
	if err := manager.Revoke(account, 1); err != nil {
		t.Fatalf("second Revoke: %v", err)
	}
}

func TestProvisionUsesExternalLimitForAllTransfers(t *testing.T) {
	manager := testManager(t)
	if err := manager.Provision("funding", 0, 123456); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	path, _ := manager.PolicyPath("funding", 0)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read policy: %v", err)
	}
	for _, field := range []string{"trade_limit=123456", "bank_limit=123456", "drop_limit=123456"} {
		if !strings.Contains(string(data), field) {
			t.Errorf("policy missing %q: %s", field, data)
		}
	}
}

func TestInvalidAccountSlotAndLimitRejected(t *testing.T) {
	manager := testManager(t)
	for _, account := range []string{"", "../escape", "a/b", "a\\b", "a\x00b", "中文", strings.Repeat("a", 16)} {
		if _, err := manager.PolicyPath(account, 0); !errors.Is(err, ErrInvalidAccount) {
			t.Errorf("PolicyPath(%q) error = %v, want ErrInvalidAccount", account, err)
		}
		if err := manager.Provision(account, 0, 1); !errors.Is(err, ErrInvalidAccount) {
			t.Errorf("Provision(%q) error = %v, want ErrInvalidAccount", account, err)
		}
	}
	for _, slot := range []int{-1, 2, 99} {
		if _, err := manager.PolicyPath("valid", slot); !errors.Is(err, ErrInvalidSlot) {
			t.Errorf("PolicyPath slot %d error = %v, want ErrInvalidSlot", slot, err)
		}
		if err := manager.Provision("valid", slot, 1); !errors.Is(err, ErrInvalidSlot) {
			t.Errorf("Provision slot %d error = %v, want ErrInvalidSlot", slot, err)
		}
	}
	for _, limit := range []int64{-1, maxPolicyLimit + 1} {
		if err := manager.Provision("valid", 0, limit); !errors.Is(err, ErrInvalidLimit) {
			t.Errorf("Provision limit %d error = %v, want ErrInvalidLimit", limit, err)
		}
	}
	if manager.Allowed("../escape", 0) || manager.Allowed("valid", 2) {
		t.Fatal("invalid identity was allowed")
	}
}

func TestPolicyTamperingAndSymlinkFailClosed(t *testing.T) {
	manager := testManager(t)
	account := "tamper"
	if err := manager.Provision(account, 0, 10); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	path, _ := manager.PolicyPath(account, 0)
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatalf("chmod policy: %v", err)
	}
	if manager.Allowed(account, 0) {
		t.Fatal("permissive policy file was allowed")
	}
	if err := manager.Provision(account, 0, 20); err != nil {
		t.Fatalf("repair policy: %v", err)
	}
	if !manager.Allowed(account, 0) {
		t.Fatal("repaired policy was not allowed")
	}

	target := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
		t.Fatalf("write outside target: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove policy: %v", err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if manager.Allowed(account, 0) {
		t.Fatal("symlink policy was allowed")
	}
	if err := manager.Provision(account, 0, 30); !errors.Is(err, ErrUnsafePolicy) {
		t.Fatalf("Provision symlink error = %v, want ErrUnsafePolicy", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read outside target: %v", err)
	}
	if string(data) != "outside" {
		t.Fatalf("outside target changed to %q", data)
	}
	if err := manager.Revoke(account, 0); err != nil {
		t.Fatalf("Revoke symlink: %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlink still exists, lstat error = %v", err)
	}
}

func TestNewDefaultManagerUsesConfiguredDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(PolicyDirEnv, dir)
	manager, err := NewDefaultManager()
	if err != nil {
		t.Fatalf("NewDefaultManager: %v", err)
	}
	if manager.Dir() != dir {
		t.Fatalf("default manager dir = %q, want %q", manager.Dir(), dir)
	}
}
