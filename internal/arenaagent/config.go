package arenaagent

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

type MemberConfig struct {
	ID      string `json:"id"`
	Config  string `json:"config"`
	Socket  string `json:"socket"`
	PetMask *int   `json:"pet_mask,omitempty"`
}
type Config struct {
	Schema       int            `json:"schema_version"`
	Sactl        string         `json:"sactl"`
	StateDir     string         `json:"state_dir"`
	OwnershipDir string         `json:"ownership_dir,omitempty"`
	Mode         int            `json:"mode"`
	Strategy     string         `json:"strategy"`
	Fallback     string         `json:"fallback"`
	Model        string         `json:"model,omitempty"`
	Members      []MemberConfig `json:"members"`
	LLM          Object         `json:"llm,omitempty"`
	Plugins      []Object       `json:"plugins,omitempty"`
}

func absolute(base, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(base, path)
}
func LoadConfig(path string) (Config, error) {
	var c Config
	b, e := os.ReadFile(path)
	if e != nil {
		return c, e
	}
	if e = decode(b, &c); e != nil {
		return c, e
	}
	base, e := filepath.Abs(filepath.Dir(path))
	if e != nil {
		return c, e
	}
	if c.Schema != 1 || c.Mode < 1 || c.Mode > 5 || len(c.Members) != c.Mode || c.StateDir == "" {
		return c, fmt.Errorf("schema_version=1, state_dir and exactly 1–5 members matching mode are required")
	}
	if c.Strategy == "" {
		c.Strategy = "basic"
	}
	if c.Fallback == "" {
		c.Fallback = "basic"
	}
	if c.Fallback != "basic" {
		return c, fmt.Errorf("fallback must be basic")
	}
	if c.Sactl == "" || c.Sactl == "sactl" {
		c.Sactl, e = os.Executable()
		if e != nil {
			return c, e
		}
	}
	if filepath.Base(c.Sactl) != c.Sactl {
		c.Sactl = absolute(base, c.Sactl)
	}
	c.StateDir = absolute(base, c.StateDir)
	if c.OwnershipDir != "" {
		c.OwnershipDir = absolute(base, c.OwnershipDir)
	} else {
		dir, e := os.UserCacheDir()
		if e != nil {
			return c, e
		}
		c.OwnershipDir = filepath.Join(dir, "stoneage-arena", "ownership")
	}
	ids, sockets := map[string]bool{}, map[string]bool{}
	valid := regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	for i := range c.Members {
		m := &c.Members[i]
		if !valid.MatchString(m.ID) || ids[m.ID] || m.Config == "" || m.Socket == "" {
			return c, fmt.Errorf("members require unique lowercase IDs, configs and sockets")
		}
		m.Config = absolute(base, m.Config)
		m.Socket = absolute(base, m.Socket)
		if sockets[m.Socket] {
			return c, fmt.Errorf("members cannot share sockets")
		}
		ids[m.ID] = true
		sockets[m.Socket] = true
		if m.PetMask != nil && (*m.PetMask < 0 || *m.PetMask > 31) {
			return c, fmt.Errorf("pet_mask must be 0–31")
		}
	}
	if c.Strategy == "learned" || c.Strategy == "hybrid" {
		if c.Model == "" {
			return c, fmt.Errorf("strategy requires model")
		}
		c.Model = absolute(base, c.Model)
	}
	return c, nil
}
