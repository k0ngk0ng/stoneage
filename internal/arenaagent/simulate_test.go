package arenaagent

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestSimulationNativeDirectoryPreservesEngineSelection(t *testing.T) {
	root := t.TempDir()
	s := Simulation{Root: root, Work: "build/fixture", Mode: 1, Matches: 1, Strategy: "basic"}
	defaulted, err := s.validate()
	if err != nil || defaulted.nativeRoot() != filepath.Join(root, "build/local-arena/native") {
		t.Fatal("default native binaries changed", defaulted.nativeRoot(), err)
	}
	s.NativeDirectory = "build/another-engine"
	selected, err := s.validate()
	if err != nil || selected.nativeRoot() != filepath.Join(root, s.NativeDirectory) {
		t.Fatal("explicit engine not selected", selected.nativeRoot(), err)
	}
	args := selected.containerArguments()
	found := false
	for i, arg := range args {
		if arg == "--native-dir" {
			found = true
			if args[i+1] != "/repo/build/another-engine" {
				t.Fatal("container did not receive chosen engine", args)
			}
		}
	}
	if !found {
		t.Fatal("container silently uses default engine")
	}
	for _, invalid := range []string{"build", "build/../../external", "elsewhere"} {
		s.NativeDirectory = invalid
		if _, err := s.validate(); err == nil {
			t.Fatal("unmounted native directory accepted", invalid)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "build/real"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(root, "build/link")); err != nil {
		t.Fatal(err)
	}
	for _, linked := range []string{"build/link", "build/link/child"} {
		s.NativeDirectory = linked
		if _, err := s.validate(); err == nil {
			t.Fatal("symlinked native directory accepted", linked)
		}
	}
}

func TestSimulationBothCommandersReceiveRequestedRoster(t *testing.T) {
	allocations := []string{"0,20,0,0", "14,4,2,0", "0,0,0,20", "5,5,5,5", "0,0,20,0"}
	keys := []string{"Vital", "Strength", "Toughness", "Dexterity"}
	for mode := 1; mode <= 5; mode++ {
		t.Run(strconv.Itoa(mode), func(t *testing.T) {
			s := Simulation{Mode: mode, MemberAllocations: allocations[:mode]}
			before := map[int]Object{}
			for index := 0; index < 2*mode; index++ {
				args, err := s.memberCreationArgs("fixture", index)
				if err != nil {
					t.Fatal(err)
				}
				player := Object{"CombatStatsKnown": true}
				for j, key := range keys {
					player[key], err = strconv.Atoi(args[3+2*j])
					if err != nil {
						t.Fatal(err)
					}
				}
				before[index] = Object{"Player": player}
			}
			if evidence, err := s.initialRosterEvidence(before); err != nil || len(evidence) != 2*mode {
				t.Fatal("created rosters fail independent public observation check", evidence, err)
			}
			// This was the old 2v2 pairing: two all-strength players against
			// two defensive players. Both rosters still satisfy a 20-point sum.
			if mode == 2 {
				before[1], before[2] = before[2], before[1]
				if _, err := s.initialRosterEvidence(before); err == nil {
					t.Fatal("old asymmetric roster accepted as a fair fixture")
				}
				before[1], before[2] = before[2], before[1]
			}
			obj(before[0]["Player"])["CombatStatsKnown"] = false
			if _, err := s.initialRosterEvidence(before); err == nil {
				t.Fatal("unknown attributes accepted")
			}
			obj(before[0]["Player"])["CombatStatsKnown"] = true
			delete(obj(before[0]["Player"]), "Vital")
			if _, err := s.initialRosterEvidence(before); err == nil {
				t.Fatal("missing zero-valued attribute accepted")
			}
			if _, err := s.memberCreationArgs("fixture", 2*mode); err == nil {
				t.Fatal("out-of-range member accepted")
			}
		})
	}
}

func TestSimulationCreationUsesNormalIntegerBudget(t *testing.T) {
	s := Simulation{Allocation: "1,17,1,1"}
	args, err := s.creationArgs("fixture", 0)
	want := []string{"create-character", "fixture", "--vital", "1", "--strength", "17", "--toughness", "1", "--dexterity", "1"}
	if err != nil || !reflect.DeepEqual(args, want) {
		t.Fatalf("normal creation flags: %v %v", args, err)
	}
	for _, invalid := range []string{"1,17,1", "1,17,1,2", "-1,19,1,1", "0,21,0,-1", "1,17.0,1,1", "1,17,1,1,0"} {
		if _, err := (Simulation{Allocation: invalid}).creationArgs("fixture", 0); err == nil {
			t.Fatalf("invalid native budget accepted: %s", invalid)
		}
	}
	args, err = (Simulation{}).creationArgs("fixture", 0)
	if err != nil || !reflect.DeepEqual(args, []string{"create-character", "fixture"}) {
		t.Fatal("default simulation changed the server's creation defaults")
	}
	mixed := Simulation{Mode: 2, MemberAllocations: []string{"1,17,1,1", "10,4,3,3"}}
	args, err = mixed.creationArgs("fixture", 0)
	if err != nil || !reflect.DeepEqual(args, want) {
		t.Fatal("first roster allocation not used", args, err)
	}
	args, err = mixed.creationArgs("fixture", 1)
	if err != nil || args[3] != "10" || args[5] != "4" {
		t.Fatal("second roster allocation not used", args, err)
	}
	mixed.Mode = 3
	if _, err = mixed.creationArgs("fixture", 0); err == nil {
		t.Fatal("incomplete per-seat allocation accepted")
	}
	mixed.Mode, mixed.Allocation = 2, "5,5,5,5"
	if _, err = mixed.creationArgs("fixture", 0); err == nil {
		t.Fatal("ambiguous uniform/per-seat allocation accepted")
	}
}

func TestSimulationTimeoutIncludesSetupAndSurvivesContainerBoundary(t *testing.T) {
	for _, tc := range []struct {
		matches         int
		requested, want time.Duration
	}{{2, 0, 10 * time.Minute}, {7, 0, 14 * time.Minute}, {2, 17 * time.Second, 17 * time.Second}} {
		s, err := (Simulation{Root: t.TempDir(), Work: "build/fixture", Mode: 2, Matches: tc.matches, Strategy: "basic", Timeout: tc.requested}).validate()
		if err != nil || s.Timeout != tc.want {
			t.Fatal("incorrect total fixture budget", s.Timeout, err)
		}
		args := s.containerArguments()
		found := false
		for i, arg := range args {
			if arg == "--timeout" {
				found = true
				d, err := time.ParseDuration(args[i+1])
				if err != nil || d != tc.want {
					t.Fatal("container lost explicit duration", args)
				}
			}
		}
		if !found {
			t.Fatal("native runner silently falls back to another timeout")
		}
	}
	if _, err := (Simulation{Root: t.TempDir(), Work: "build/fixture", Mode: 2, Matches: 2, Strategy: "basic", Timeout: -time.Second}).validate(); err == nil {
		t.Fatal("negative timeout accepted")
	}
}
