package battletrain

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
)

type DemonstrationManifest struct {
	Schema      string   `json:"schema"`
	Digest      string   `json:"sha256"`
	Bytes       int64    `json:"bytes"`
	Episodes    int      `json:"episodes"`
	Turns       int      `json:"team_turns"`
	Actions     int      `json:"written_choices"`
	Sources     []string `json:"source_databases"`
	Groups      []string `json:"roster_groups"`
	Rules       []string `json:"rules"`
	Modes       []int    `json:"modes"`
	Features    []string `json:"features"`
	OnPolicyPPO bool     `json:"on_policy_ppo"`
}

func describeDemonstrations(ctx context.Context, episodes []Demonstration) (DemonstrationManifest, error) {
	m := DemonstrationManifest{Schema: "commander-demonstration-set-v1", Episodes: len(episodes)}
	if len(episodes) < 1 || len(episodes) > 10000 {
		return m, fmt.Errorf("demonstration set requires 1..10000 matches")
	}
	sources, groups, rules, features, modes, seen := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}, map[int]bool{}, map[string]bool{}
	for _, d := range episodes {
		if err := ctx.Err(); err != nil {
			return m, err
		}
		if err := d.Validate(); err != nil {
			return m, err
		}
		key := fmt.Sprintf("%s:%d", d.Match, d.Side)
		if seen[key] {
			return m, fmt.Errorf("duplicate match side in demonstration set")
		}
		seen[key] = true
		sources[d.Source], groups[d.Group], rules[d.Rules], features[d.Features], modes[d.Mode] = true, true, true, true, true
		m.Turns += len(d.Steps)
		for _, s := range d.Steps {
			m.Actions += len(s.Choices)
		}
	}
	strings := func(values map[string]bool) []string {
		var out []string
		for v := range values {
			out = append(out, v)
		}
		sort.Strings(out)
		return out
	}
	m.Sources, m.Groups, m.Rules, m.Features = strings(sources), strings(groups), strings(rules), strings(features)
	for mode := range modes {
		m.Modes = append(m.Modes, mode)
	}
	sort.Ints(m.Modes)
	return m, nil
}

// A dataset is visible to LoadDemonstrations only after its matching manifest
// is published. Interrupted/orphan files are preserved, never overwritten.
func SaveDemonstrations(ctx context.Context, path string, episodes []Demonstration) (DemonstrationManifest, error) {
	m, err := describeDemonstrations(ctx, episodes)
	if err != nil {
		return m, err
	}
	for _, p := range []string{path, path + ".manifest.json"} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			return m, fmt.Errorf("demonstration output must be new")
		}
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".demonstrations-*")
	if err != nil {
		return m, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	h := sha256.New()
	writer := io.MultiWriter(f, h)
	for _, d := range episodes {
		if err := ctx.Err(); err != nil {
			return m, err
		}
		b, err := json.Marshal(d)
		if err != nil {
			return m, err
		}
		if len(b) >= 64<<20 || m.Bytes+int64(len(b))+1 > 256<<20 {
			return m, fmt.Errorf("demonstration dataset exceeds size limit")
		}
		if _, err := writer.Write(append(b, '\n')); err != nil {
			return m, err
		}
		m.Bytes += int64(len(b)) + 1
	}
	m.Digest = hex.EncodeToString(h.Sum(nil))
	if err := f.Sync(); err != nil {
		return m, err
	}
	if err := f.Close(); err != nil {
		return m, err
	}
	if err := ctx.Err(); err != nil {
		return m, err
	}
	if err := os.Link(f.Name(), path); err != nil {
		return m, err
	}
	return m, writeObject(path+".manifest.json", m)
}

func LoadDemonstrations(ctx context.Context, path string) ([]Demonstration, DemonstrationManifest, error) {
	var m DemonstrationManifest
	if err := readObject(path+".manifest.json", &m, 4<<20); err != nil {
		return nil, m, err
	}
	if m.Schema != "commander-demonstration-set-v1" || !digest(m.Digest) || m.Bytes < 1 || m.Bytes > 256<<20 || m.Episodes < 1 || m.Episodes > 10000 || m.OnPolicyPPO {
		return nil, m, fmt.Errorf("invalid demonstration manifest")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, m, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, m, err
	}
	if !info.Mode().IsRegular() || info.Size() != m.Bytes {
		return nil, m, fmt.Errorf("demonstration data size mismatch")
	}
	h := sha256.New()
	scanner := bufio.NewScanner(io.TeeReader(io.LimitReader(f, m.Bytes+1), h))
	scanner.Buffer(make([]byte, 4096), 64<<20)
	var episodes []Demonstration
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, m, err
		}
		if len(episodes) >= m.Episodes {
			return nil, m, fmt.Errorf("too many demonstration episodes")
		}
		var d Demonstration
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&d); err != nil {
			return nil, m, err
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			return nil, m, fmt.Errorf("trailing demonstration JSON")
		}
		episodes = append(episodes, d)
	}
	if err := scanner.Err(); err != nil {
		return nil, m, err
	}
	if hex.EncodeToString(h.Sum(nil)) != m.Digest {
		return nil, m, fmt.Errorf("demonstration data checksum mismatch")
	}
	want, err := describeDemonstrations(ctx, episodes)
	if err != nil {
		return nil, m, err
	}
	want.Digest, want.Bytes = m.Digest, m.Bytes
	if !reflect.DeepEqual(want, m) {
		return nil, m, fmt.Errorf("demonstration manifest differs from contents")
	}
	return episodes, m, nil
}
