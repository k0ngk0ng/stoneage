package arenaagent

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/sacli"
)

func TestOnlineFlagsNeedNoConfigurationOrCredentials(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	f := flag.NewFlagSet("run", flag.ContinueOnError)
	o := onlineFlags{}
	o.bind(f)
	if e := f.Parse([]string{"--profile", "bot", "--strategy", "llm", "--llm-endpoint", "http://localhost:8000/v1/chat/completions", "--llm-model", "fixture", "--pet-mask", "3"}); e != nil {
		t.Fatal(e)
	}
	c, e := o.config("", f)
	if e != nil {
		t.Fatal(e)
	}
	if c.Mode != 1 || c.Strategy != "llm" || c.Members[0].Profile != "bot" || c.Members[0].Config != "" || *c.Members[0].PetMask != 3 || str(c.LLM["model"]) != "fixture" {
		t.Fatalf("wrong options: %+v", c)
	}
	if _, e = os.Stat(root + "/config"); !os.IsNotExist(e) {
		t.Fatal("configuration was written")
	}
	if _, e = os.Stat(root + "/state"); !os.IsNotExist(e) {
		t.Fatal("state was written by parsing")
	}
	if _, e = o.config("missing-config", f); e == nil || !strings.Contains(e.Error(), "cannot be combined") {
		t.Fatal(e)
	}
	// Current profile is respected, without requiring a profile TOML.
	if e = sacli.SelectProfile("chosen"); e != nil {
		t.Fatal(e)
	}
	f = flag.NewFlagSet("check", flag.ContinueOnError)
	o = onlineFlags{}
	o.bind(f)
	f.Parse(nil)
	c, e = o.config("", f)
	if e != nil || c.Members[0].Profile != "chosen" {
		t.Fatal(c, e)
	}
}
func TestOnlineFlagsRejectInvalidOrIgnoredOptions(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for _, args := range [][]string{
		{"--profile", "bot", "--profile", "bot"}, {"--profile", "../bad"}, {"--profile", "one", "--mode", "2"}, {"--profile", "one", "--pet-mask", "32"}, {"--profile", "one", "--strategy", "learned"}, {"--profile", "one", "--state-dir", ""},
	} {
		f := flag.NewFlagSet("run", flag.ContinueOnError)
		o := onlineFlags{}
		o.bind(f)
		if e := f.Parse(args); e != nil {
			t.Fatal(e)
		}
		if _, e := o.config("", f); e == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if e := Main(context.Background(), []string{"check", "--profile", "bot", "unexpected"}, "test", &bytes.Buffer{}); e == nil {
		t.Fatal("ignored positional argument")
	}
}
func TestProfileAttachmentIsReadOnlyAndFencesIdentity(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "client")
	script := `#!/bin/sh
set -eu
[ "$1" = --socket ] && [ "$3" = --json ] || exit 1
root="$(dirname "$2")"
printf '%s\n' "$4" >> "$root/calls"
case "$4" in
 ping) cat "$root/ping.json" ;;
 status) cat "$root/status.json" ;;
 *) exit 1 ;;
esac
`
	if e := os.WriteFile(binary, []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	write := func(name string, v Object) {
		t.Helper()
		if e := os.WriteFile(filepath.Join(root, name+".json"), enc(Object{"ok": true, "data": v}), 0600); e != nil {
			t.Fatal(e)
		}
	}
	ping := Object{"interactive_login": true, "transport": "http", "endpoint": "https://example.com", "server_id": "line-1"}
	status := Object{"Account": "fixture-account", "Character": "fixture-char", "Player": Object{"HasStatus": true}}
	write("ping", ping)
	write("status", status)
	store, e := OpenStore(filepath.Join(root, "data"))
	if e != nil {
		t.Fatal(e)
	}
	defer store.DB.Close()
	makeMember := func() *member {
		return &member{cfg: MemberConfig{ID: "member-1", Profile: "bot", Socket: filepath.Join(root, "bot.sock")}, binary: binary, ownership: filepath.Join(root, "ownership"), store: store}
	}
	m := makeMember()
	defer m.close()
	if e = m.start(context.Background()); e != nil {
		t.Fatal(e)
	}
	if m.process != nil || str(m.login["character"]) != "fixture-char" {
		t.Fatal("did not reuse login")
	}
	m.close()
	// A new commander cannot reuse pending state after the profile changes identity.
	status["Character"] = "other-char"
	write("status", status)
	m = makeMember()
	defer m.close()
	if e = m.start(context.Background()); e == nil || !strings.Contains(e.Error(), "another character") {
		t.Fatal(e)
	}
	m.close()
	// Older daemons fail before status/observe or any game-changing command.
	delete(ping, "interactive_login")
	write("ping", ping)
	m = makeMember()
	defer m.close()
	if e = m.start(context.Background()); e == nil || !strings.Contains(e.Error(), "unsupported daemon") {
		t.Fatal(e)
	}
	m.close()
	calls, e := os.ReadFile(filepath.Join(root, "calls"))
	if e != nil {
		t.Fatal(e)
	}
	if string(calls) != "ping\nstatus\nping\nstatus\nping\n" {
		t.Fatalf("unexpected commands: %s", calls)
	}
}
