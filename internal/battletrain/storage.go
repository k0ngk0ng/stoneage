package battletrain

import (
	"bufio"
	"compress/gzip"
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

const ShardSchema = "commander-shard-v2"

type Manifest struct {
	Schema    string   `json:"schema"`
	Digest    string   `json:"digest"`
	Bytes     int64    `json:"bytes"`
	Episodes  int      `json:"episodes"`
	TeamTurns int      `json:"team_turns"`
	Groups    []string `json:"groups"`
	Policies  []string `json:"policies"`
	Rules     []string `json:"rules"`
}

func describe(episodes []Trajectory) (Manifest, error) {
	m := Manifest{Schema: ShardSchema, Episodes: len(episodes)}
	if len(episodes) == 0 || len(episodes) > 100000 {
		return m, fmt.Errorf("shard requires 1..100000 trajectories")
	}
	groups, policies, rules, identities := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, t := range episodes {
		if e := t.Validate(); e != nil {
			return m, e
		}
		key := fmt.Sprintf("%s:%d", t.Match, t.Side)
		if identities[key] {
			return m, fmt.Errorf("duplicate trajectory in shard")
		}
		identities[key] = true
		groups[t.Group], policies[t.Policy], rules[t.Rules] = true, true, true
		m.TeamTurns += len(t.Steps)
	}
	for k := range groups {
		m.Groups = append(m.Groups, k)
	}
	sort.Strings(m.Groups)
	for k := range policies {
		m.Policies = append(m.Policies, k)
	}
	sort.Strings(m.Policies)
	for k := range rules {
		m.Rules = append(m.Rules, k)
	}
	sort.Strings(m.Rules)
	return m, nil
}

// publish uses a same-directory hard link for no-replace atomic visibility.
// An interrupted writer leaves at most a hidden temporary/orphan shard, never
// a partially visible published file. A matching existing object is reusable.
func publish(temp, path string) error {
	if e := os.Link(temp, path); e != nil {
		if !os.IsExist(e) {
			return e
		}
		a, e := fileHash(temp)
		if e != nil {
			return e
		}
		b, e := fileHash(path)
		if e != nil {
			return e
		}
		if a != b {
			return fmt.Errorf("refusing to overwrite different content: %s", filepath.Base(path))
		}
	}
	d, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func fileHash(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func writeObject(path string, object any) error {
	f, e := os.CreateTemp(filepath.Dir(path), ".write-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if e = json.NewEncoder(f).Encode(object); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return publish(f.Name(), path)
}

// SaveShard writes immutable gzip JSONL, then publishes its manifest last.
// Runtime event logs remain separate. Neither in-progress files nor orphaned
// shards without a manifest are discoverable as completed training data.
func SaveShard(directory string, episodes []Trajectory) (Manifest, error) {
	m, e := describe(episodes)
	if e != nil {
		return m, e
	}
	if e = os.MkdirAll(directory, 0700); e != nil {
		return m, e
	}
	f, e := os.CreateTemp(directory, ".shard-*")
	if e != nil {
		return m, e
	}
	defer os.Remove(f.Name())
	defer f.Close()
	h := sha256.New()
	compressed := gzip.NewWriter(io.MultiWriter(f, h))
	var uncompressed int64
	for _, t := range episodes {
		var record []byte
		record, e = json.Marshal(t)
		if e != nil {
			compressed.Close()
			return m, e
		}
		uncompressed += int64(len(record)) + 1
		if len(record) >= 64<<20 || uncompressed > 8<<30 {
			compressed.Close()
			return m, fmt.Errorf("shard exceeds record/decompression limit")
		}
		if _, e = compressed.Write(append(record, '\n')); e != nil {
			compressed.Close()
			return m, e
		}
	}
	if e = compressed.Close(); e != nil {
		return m, e
	}
	if e = f.Sync(); e != nil {
		return m, e
	}
	info, e := f.Stat()
	if e != nil {
		return m, e
	}
	m.Bytes = info.Size()
	if m.Bytes > 4<<30 {
		return m, fmt.Errorf("compressed shard exceeds byte limit")
	}
	m.Digest = hex.EncodeToString(h.Sum(nil))
	if e = f.Close(); e != nil {
		return m, e
	}
	if e = publish(f.Name(), filepath.Join(directory, m.Digest+".jsonl.gz")); e != nil {
		return m, e
	}
	if e = writeObject(filepath.Join(directory, m.Digest+".manifest.json"), m); e != nil {
		return m, e
	}
	return m, nil
}

func readObject(path string, object any, limit int64) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return e
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return fmt.Errorf("object exceeds regular file size limit")
	}
	dec := json.NewDecoder(io.LimitReader(f, limit+1))
	dec.DisallowUnknownFields()
	if e = dec.Decode(object); e != nil {
		return e
	}
	var extra any
	if e = dec.Decode(&extra); e != io.EOF {
		return fmt.Errorf("trailing object content")
	}
	return nil
}

// LoadShard verifies compressed bytes BEFORE decoding bounded records. It
// validates terminal semantics/masks, then independently rebuilds the manifest.
func LoadShard(directory, id string) ([]Trajectory, Manifest, error) {
	var m Manifest
	if !digest(id) {
		return nil, m, fmt.Errorf("invalid shard identity")
	}
	if e := readObject(filepath.Join(directory, id+".manifest.json"), &m, 4<<20); e != nil {
		return nil, m, e
	}
	if m.Schema != ShardSchema || m.Digest != id || m.Episodes < 1 || m.Episodes > 100000 || m.Bytes < 1 || m.Bytes > 4<<30 {
		return nil, m, fmt.Errorf("invalid shard manifest")
	}
	path := filepath.Join(directory, id+".jsonl.gz")
	f, e := os.Open(path)
	if e != nil {
		return nil, m, e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return nil, m, e
	}
	if !info.Mode().IsRegular() || info.Size() != m.Bytes {
		return nil, m, fmt.Errorf("shard byte count mismatch")
	}
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return nil, m, e
	}
	if hex.EncodeToString(h.Sum(nil)) != id {
		return nil, m, fmt.Errorf("shard checksum mismatch")
	}
	if _, e = f.Seek(0, io.SeekStart); e != nil {
		return nil, m, e
	}
	z, e := gzip.NewReader(f)
	if e != nil {
		return nil, m, e
	}
	defer z.Close()
	limited := &io.LimitedReader{R: z, N: (8 << 30) + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 65536), 64<<20)
	var episodes []Trajectory
	for scanner.Scan() {
		if len(episodes) >= m.Episodes {
			return nil, m, fmt.Errorf("too many shard records")
		}
		var t Trajectory
		if e = json.Unmarshal(scanner.Bytes(), &t); e != nil {
			return nil, m, e
		}
		episodes = append(episodes, t)
	}
	if e = scanner.Err(); e != nil {
		return nil, m, e
	}
	if limited.N == 0 {
		return nil, m, fmt.Errorf("shard exceeds decompression limit")
	}
	actual, e := describe(episodes)
	if e != nil {
		return nil, m, e
	}
	actual.Digest, actual.Bytes = id, m.Bytes
	if !reflect.DeepEqual(actual, m) {
		return nil, m, fmt.Errorf("shard manifest does not describe its records")
	}
	return episodes, m, nil
}
