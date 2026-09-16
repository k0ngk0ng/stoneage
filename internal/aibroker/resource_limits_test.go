package aibroker

import (
	"errors"
	"math"
	"reflect"
	"strconv"
	"testing"
)

func TestBrokerAppliesDefaultRuntimeResourceLimits(t *testing.T) {
	broker := newFakeBroker(t, &fakeDocker{}, NewMemoryJournal())
	config := broker.Config()
	if config.CPUs != DefaultCPUs || config.Memory != DefaultMemory || config.MemorySwap != DefaultMemorySwap || config.PidsLimit != DefaultPidsLimit {
		t.Fatalf("default config limits=(%v,%q,%q,%d), want=(%v,%q,%q,%d)", config.CPUs, config.Memory, config.MemorySwap, config.PidsLimit, DefaultCPUs, DefaultMemory, DefaultMemorySwap, DefaultPidsLimit)
	}
	spec := broker.runSpec("profile-1", broker.VolumeName("profile-1"), broker.ContainerName("profile-1", "request-1"))
	if spec.CPUs != DefaultCPUs || spec.Memory != DefaultMemory || spec.MemorySwap != DefaultMemorySwap || spec.PidsLimit != DefaultPidsLimit {
		t.Fatalf("default run spec limits=(%v,%q,%q,%d)", spec.CPUs, spec.Memory, spec.MemorySwap, spec.PidsLimit)
	}
}

func TestBrokerKeepsConfiguredRuntimeResourceLimitsInRunSpec(t *testing.T) {
	broker, err := New(Config{
		Docker:     &fakeDocker{},
		Journal:    NewMemoryJournal(),
		Image:      "registry.example/stoneage-ai@sha256:abc",
		Network:    "stoneage-backend",
		CPUs:       1.25,
		Memory:     "256m",
		MemorySwap: "768m",
		PidsLimit:  64,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := Config{CPUs: 1.25, Memory: "256m", MemorySwap: "768m", PidsLimit: 64}
	got := broker.Config()
	if got.CPUs != want.CPUs || got.Memory != want.Memory || got.MemorySwap != want.MemorySwap || got.PidsLimit != want.PidsLimit {
		t.Fatalf("configured config limits=(%v,%q,%q,%d), want=(%v,%q,%q,%d)", got.CPUs, got.Memory, got.MemorySwap, got.PidsLimit, want.CPUs, want.Memory, want.MemorySwap, want.PidsLimit)
	}
	spec := broker.runSpec("profile-1", broker.VolumeName("profile-1"), broker.ContainerName("profile-1", "request-1"))
	if spec.CPUs != want.CPUs || spec.Memory != want.Memory || spec.MemorySwap != want.MemorySwap || spec.PidsLimit != want.PidsLimit {
		t.Fatalf("configured run spec limits=(%v,%q,%q,%d), want=(%v,%q,%q,%d)", spec.CPUs, spec.Memory, spec.MemorySwap, spec.PidsLimit, want.CPUs, want.Memory, want.MemorySwap, want.PidsLimit)
	}
}

func TestDockerArgsIncludeRuntimeResourceLimits(t *testing.T) {
	spec := cliTestSpec(t)
	args := dockerArgs(spec)
	want := []string{"--cpus", "0.5", "--memory", "512m", "--memory-swap", "512m", "--pids-limit", "128"}
	for index := 0; index+len(want) <= len(args); index++ {
		if reflect.DeepEqual(args[index:index+len(want)], want) {
			return
		}
	}
	t.Fatalf("Docker args=%v, want resource options=%v", args, want)
}

func TestBrokerRejectsUnsafeRuntimeResourceLimits(t *testing.T) {
	cases := []Config{
		{CPUs: -1, Memory: DefaultMemory, MemorySwap: DefaultMemorySwap, PidsLimit: DefaultPidsLimit},
		{CPUs: math.NaN(), Memory: DefaultMemory, MemorySwap: DefaultMemorySwap, PidsLimit: DefaultPidsLimit},
		{CPUs: DefaultCPUs, Memory: "512m;--privileged", MemorySwap: DefaultMemorySwap, PidsLimit: DefaultPidsLimit},
		{CPUs: DefaultCPUs, Memory: "512m", MemorySwap: "256m", PidsLimit: DefaultPidsLimit},
		{CPUs: DefaultCPUs, Memory: DefaultMemory, MemorySwap: "-1", PidsLimit: DefaultPidsLimit},
		{CPUs: DefaultCPUs, Memory: DefaultMemory, MemorySwap: DefaultMemorySwap, PidsLimit: -1},
	}
	for index, values := range cases {
		values.Docker = &fakeDocker{}
		values.Journal = NewMemoryJournal()
		values.Image = "registry.example/stoneage-ai@sha256:abc"
		values.Network = "stoneage-backend"
		if _, err := New(values); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("case %d error=%v, want ErrInvalidConfig", index, err)
		}
	}
}

func TestDockerArgsUseConfiguredRuntimeResourceLimits(t *testing.T) {
	broker, err := New(Config{
		Docker:     &fakeDocker{},
		Journal:    NewMemoryJournal(),
		Image:      "registry.example/stoneage-ai@sha256:abc",
		Network:    "stoneage-backend",
		CPUs:       2,
		Memory:     "1g",
		MemorySwap: "2g",
		PidsLimit:  256,
	})
	if err != nil {
		t.Fatal(err)
	}
	spec := broker.runSpec("profile-1", broker.VolumeName("profile-1"), broker.ContainerName("profile-1", "request-1"))
	args := dockerArgs(spec)
	want := []string{"--cpus", "2", "--memory", "1g", "--memory-swap", "2g", "--pids-limit", strconv.Itoa(256)}
	for index := 0; index+len(want) <= len(args); index++ {
		if reflect.DeepEqual(args[index:index+len(want)], want) {
			return
		}
	}
	t.Fatalf("Docker args=%v, want configured resource options=%v", args, want)
}
