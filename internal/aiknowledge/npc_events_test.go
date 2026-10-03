package aiknowledge

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNPCEventsPreserveOrderGroupsAndSource(t *testing.T) {
	raw := []byte("# quest\r\nNomalMainMsg:hello\r\nEventNo:5\r\nTYPE:ACCEPT\r\nEVENT:NOWEV=5&ITEM=2423,ENDEV=3&SP=0\r\nGetItem:2448\r\nDelItem:2423\r\nThanksMsg:first\\npage:detail#literal\r\nThanksMsg:second\r\nEventEnd\r\nEventNo:3|TYPE:REQUEST|EVENT:LV>0|GetItem:2416|EventEnd\r\n")
	script, issues, err := parseNPCEvents(raw, "npc/axe")
	if err != nil || len(issues) != 0 {
		t.Fatal(issues, err)
	}
	if len(script.Rules) != 2 || script.Rules[0].Number != 5 || script.Rules[1].Number != 3 || len(script.Defaults) != 1 {
		t.Fatal(script)
	}
	r := script.Rules[0]
	if r.Type != "ACCEPT" || !reflect.DeepEqual(r.Alternatives, [][]string{{"NOWEV=5", "ITEM=2423"}, {"ENDEV=3", "SP=0"}}) {
		t.Fatal(r)
	}
	if value, _ := r.Field("ThanksMsg"); value != "first\\npage:detail#literal" || len(r.Fields) != 7 {
		t.Fatal("lost literal text or duplicate source field", r)
	}
	if r.Source.Line != 3 || r.Source.LineEnd != 10 || r.Source.SHA256 != SHA256Hex(raw) || r.Fields[2].Source.Line != 5 {
		t.Fatal("lost source provenance", r)
	}
}

func TestNPCEventsRejectAmbiguousOrTruncatedSources(t *testing.T) {
	for _, raw := range []string{
		"EventNo:1\nTYPE:REQUEST\nEVENT:LV>0", // truncated
		"EventNo:1\nTYPE:REQUEST\nEVENT:LV>0\nEventNo:2\nEventEnd",
		"EventNo:1\nTYPE:REQUEST\nEventEnd",
		"EventNo:1\nTYPE:REQUEST\nEVENT:LV>0,\nEventEnd",
		"EventNo:1\nTYPE:REQUEST\nEVENT:LV>0&&ITEM=1\nEventEnd",
		"EventNo:no\nTYPE:REQUEST\nEVENT:LV>0\nEventEnd",
		"#EventNo:10\nEventNo:1\nTYPE:REQUEST\nEVENT:LV>0\nEventEnd",
		"EventNo:1\nTYPE:REQUEST\nEVENT:LV>0\n\x00\nEventEnd",
	} {
		if script, _, err := parseNPCEvents([]byte(raw), "fixture"); err == nil || script != nil {
			t.Fatalf("accepted ambiguous source %q", raw)
		}
	}
}

func TestNPCEventsKeepCommentedNativeControl(t *testing.T) {
	raw := []byte("#EventNo:-1\n#TYPE:ACCEPT\n#EVENT:ITEM=20064\n#GetStone:4000\n#EventEnd\n")
	script, _, err := parseNPCEvents(raw, "commented")
	if err != nil || len(script.Rules) != 1 || !script.Rules[0].CommentedControl {
		t.Fatal(script, err)
	}
	if value, _ := script.Rules[0].Field("GetStone"); value != "4000" || !script.Rules[0].Fields[3].Commented {
		t.Fatal("commented control was silently discarded", script)
	}
}

func TestNPCEventsPreserveBreakAndDoNotJoinBareTextToDialogue(t *testing.T) {
	raw := []byte("EventNo:-1\nTYPE:ACCEPT\nEVENT:ITEM=2700\nAcceptMsg:first line\nsecond line\nBreak\nEventEnd\n")
	script, _, err := parseNPCEvents(raw, "native-break")
	if err != nil {
		t.Fatal(err)
	}
	r := script.Rules[0]
	if msg, _ := r.Field("AcceptMsg"); msg != "first line" {
		t.Fatal(msg)
	}
	if value, found := r.Field("Break"); !found || value != "" || !r.Fields[len(r.Fields)-1].Bare {
		t.Fatal(r)
	}
}

func TestExtensionlessNPCEventsLoadWithAuditableBranches(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "npc", "quest")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	raw := []byte("EventNo:3\nTYPE:REQUEST\nEVENT:LV>0\nGetItem:2416\nEventEnd\n")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	npc, files, issues, err := loadNPC(context.Background(), root, nil, nil, true)
	if err != nil || len(issues) != 0 || len(npc.Files) != 1 || len(files) != 1 {
		t.Fatal(npc, files, issues, err)
	}
	f := npc.Files[0]
	if f.Kind != "event" || !f.Supported || f.Events == nil || len(f.Events.Rules) != 1 || files[0].Records != 1 || f.SHA256 != SHA256Hex(raw) {
		t.Fatal(f, files)
	}
}

func TestNPCEventParserAgainstNativeQuestSources(t *testing.T) {
	root := "../../server/legacy/source/2.5/gmsv/data"
	for _, name := range []string{"npc/sainasu/event/event03_1", "npc/sainasu/event/event03_2", "npc/jaruga/event/event01_1", "npc/jaruga/event/event01_2", "npc/extra/event/M_1000", "npc/extra/event/M_2000", "npc/extra/event/M_3000", "npc/extra/event/M_4000"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(root, name))
			if err != nil {
				t.Fatal(err)
			}
			script, _, err := parseNPCEvents(raw, name)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasSuffix(name, "event03_2") {
				if len(script.Rules) != 3 || script.Rules[0].Number != 5 || script.Rules[1].Number != 3 || script.Rules[2].Number != -1 {
					t.Fatal("native branch priority lost", script)
				}
				if item, _ := script.Rules[0].Field("GetItem"); item != "2448" {
					t.Fatal("axe completion reward lost", item)
				}
			}
			if strings.Contains(name, "M_") {
				commissions := 0
				for _, r := range script.Rules {
					if r.Type == "ACCEPT" {
						commissions++
					}
				}
				if commissions < 12 {
					t.Fatal("missing pet/food commission branches", commissions)
				}
			}
		})
	}
}
