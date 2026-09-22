package aiknowledge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/gamecatalog"
)

var (
	// ErrMissingData is returned when a required gmsv data directory or core
	// table cannot be found.
	ErrMissingData = errors.New("aiknowledge: required 2.5 data is missing")
)

var coreDataFiles = []string{
	"exp.txt",
	"encount.txt",
	"enemy.txt",
	"enemybase.txt",
	"group.txt",
	"map/mapwarp.txt",
}

// Load loads a complete knowledge snapshot from the configured gmsv data
// directory.  The returned snapshot is safe to share after construction.
func Load(ctx context.Context, options Options) (*Knowledge, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	dataDir, err := findDataDir(options.DataDir)
	if err != nil {
		return nil, err
	}
	return load(ctx, dataDir, options)
}

// LoadDataDir is a convenience wrapper for callers that already have the
// exact gmsv data directory.  It still accepts repository and gmsv roots for
// parity with Load.
func LoadDataDir(ctx context.Context, dataDir string) (*Knowledge, error) {
	return Load(ctx, Options{DataDir: dataDir})
}

// LoadPath is the context-free convenience form used by command-line tools.
func LoadPath(dataDir string) (*Knowledge, error) {
	return Load(context.Background(), Options{DataDir: dataDir})
}

func findDataDir(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	root = filepath.Clean(root)
	candidates := []string{
		root,
		filepath.Join(root, "data"),
		filepath.Join(root, "gmsv", "data"),
		filepath.Join(root, "server", "legacy", "source", "2.5", "gmsv", "data"),
		filepath.Join(root, "runtime", "legacy-server", "gmsv", "data"),
	}
	for _, candidate := range candidates {
		if isDataDir(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%w below %s", ErrMissingData, root)
}

func isDataDir(directory string) bool {
	for _, name := range coreDataFiles {
		info, err := os.Stat(filepath.Join(directory, name))
		if err != nil || info.IsDir() {
			return false
		}
	}
	return true
}

func load(ctx context.Context, dataDir string, options Options) (*Knowledge, error) {
	groupFile := options.GroupFile
	if groupFile == "" {
		groupFile = "group.txt"
	}
	if filepath.Base(groupFile) != groupFile || strings.ContainsAny(groupFile, `/\\`) || !strings.HasSuffix(groupFile, ".txt") {
		return nil, fmt.Errorf("aiknowledge: invalid group table filename")
	}
	k := &Knowledge{Version: "stoneage-2.5", DataDir: dataDir, Files: make([]FileDigest, 0, len(coreDataFiles)), Issues: make([]Issue, 0)}
	contents := make(map[string][]byte)

	for _, name := range coreDataFiles {
		if name == "group.txt" {
			name = groupFile
		}
		if err := contextErr(ctx); err != nil {
			return nil, err
		}
		raw, err := os.ReadFile(filepath.Join(dataDir, name))
		if err != nil {
			return nil, fmt.Errorf("aiknowledge: read %s: %w", name, err)
		}
		contents[name] = raw
		file := FileDigest{Path: name, SHA256: SHA256Hex(raw), Bytes: int64(len(raw)), Supported: true, Encoding: detectEncoding(raw)}
		k.Files = append(k.Files, file)
	}

	var parseErr error
	if k.Experience, k.Issues, parseErr = parseExperience(contents["exp.txt"], "exp.txt", options.Strict); parseErr != nil {
		return nil, parseErr
	}
	var issues []Issue
	if k.Encounters, issues, parseErr = parseEncounters(contents["encount.txt"], "encount.txt", options.Strict); parseErr != nil {
		return nil, parseErr
	}
	k.Issues = append(k.Issues, issues...)
	if k.EnemyBases, issues, parseErr = parseEnemyBases(contents["enemybase.txt"], "enemybase.txt", options.Strict); parseErr != nil {
		return nil, parseErr
	}
	k.Issues = append(k.Issues, issues...)
	if k.EnemiesTable, issues, parseErr = parseEnemies(contents["enemy.txt"], "enemy.txt", options.Strict); parseErr != nil {
		return nil, parseErr
	}
	k.Issues = append(k.Issues, issues...)
	if k.Groups, issues, parseErr = parseGroups(contents[groupFile], groupFile, options.Strict); parseErr != nil {
		return nil, parseErr
	}
	k.Issues = append(k.Issues, issues...)
	if k.Warps, issues, parseErr = parseMapWarps(contents["map/mapwarp.txt"], "map/mapwarp.txt", options.Strict); parseErr != nil {
		return nil, parseErr
	}
	k.Issues = append(k.Issues, issues...)

	// Attach the exact digest to every row's provenance record.
	for index := range k.Files {
		if raw, ok := contents[k.Files[index].Path]; ok {
			annotateParsedSources(k, k.Files[index], raw)
		}
	}

	if k.NPC, k.Files, k.Issues, parseErr = loadNPC(ctx, dataDir, k.Files, k.Issues, options.Strict); parseErr != nil {
		return nil, parseErr
	}

	var taskFiles []FileDigest
	var taskBytes map[string][]byte
	if k.TaskDefinitions, taskFiles, taskBytes, issues, parseErr = loadTasks(options.TaskDir, dataDir, k.EnemyBases, k.NPC, options.Strict); parseErr != nil {
		if options.Strict {
			return nil, parseErr
		}
		k.Issues = append(k.Issues, Issue{Severity: SeverityWarning, Code: "task_load", Message: parseErr.Error()})
	}
	k.Files = append(k.Files, taskFiles...)
	k.Issues = append(k.Issues, issues...)

	if err := validateReferences(k, options.Strict); err != nil {
		return nil, err
	}
	k.Leveling = deriveLevelingAreas(k)
	k.Catalog = catalogFromBases(k.EnemyBases)
	k.CoverageReport = makeCoverage(k)

	// Fingerprinting uses path + raw bytes in deterministic order.  Task files
	// participate in the fingerprint so changing an evidence definition always
	// invalidates a running AI plan.
	k.Digest = fingerprint(k.Files, contents, taskBytes)
	k.Version = "stoneage-2.5/" + k.Digest[:16]
	annotateAllSources(k)
	return k, nil
}

func contextErr(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func detectEncoding(raw []byte) string {
	lines, issues := decodeLines(raw, false)
	if len(issues) > 0 && len(lines) == 0 {
		return "unsupported"
	}
	seen := map[string]bool{}
	for _, line := range lines {
		seen[line.Encoding] = true
	}
	values := make([]string, 0, len(seen))
	for value := range seen {
		values = append(values, value)
	}
	sort.Strings(values)
	return strings.Join(values, "+")
}

func fingerprint(files []FileDigest, contents, taskBytes map[string][]byte) string {
	ordered := append([]FileDigest(nil), files...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	hashInput := make([]byte, 0)
	for _, file := range ordered {
		hashInput = append(hashInput, file.Path...)
		hashInput = append(hashInput, 0)
		raw, ok := contents[file.Path]
		if !ok {
			raw = taskBytes[file.Path]
		}
		hashInput = append(hashInput, raw...)
		hashInput = append(hashInput, 0)
	}
	return SHA256Hex(hashInput)
}

func catalogFromBases(bases []EnemyBase) *gamecatalog.Catalog {
	if len(bases) == 0 {
		return nil
	}
	pets := make([]gamecatalog.Pet, 0, len(bases))
	for _, base := range bases {
		if base.Pet.Entry.Name != "" {
			pets = append(pets, base.Pet)
		}
	}
	return &gamecatalog.Catalog{Pets: pets}
}

func annotateParsedSources(k *Knowledge, digest FileDigest, _ []byte) {
	for i := range k.Experience {
		if k.Experience[i].Source.Path == digest.Path {
			annotateSource(&k.Experience[i].Source, digest)
		}
	}
	for i := range k.Encounters {
		if k.Encounters[i].Source.Path == digest.Path {
			annotateSource(&k.Encounters[i].Source, digest)
		}
	}
	for i := range k.EnemyBases {
		if k.EnemyBases[i].Source.Path == digest.Path {
			annotateSource(&k.EnemyBases[i].Source, digest)
		}
	}
	for i := range k.EnemiesTable {
		if k.EnemiesTable[i].Source.Path == digest.Path {
			annotateSource(&k.EnemiesTable[i].Source, digest)
		}
	}
	for i := range k.Groups {
		if k.Groups[i].Source.Path == digest.Path {
			annotateSource(&k.Groups[i].Source, digest)
		}
	}
	for i := range k.Warps {
		if k.Warps[i].Source.Path == digest.Path {
			annotateSource(&k.Warps[i].Source, digest)
		}
	}
}

func annotateAllSources(k *Knowledge) {
	byPath := make(map[string]FileDigest, len(k.Files))
	for _, file := range k.Files {
		byPath[file.Path] = file
	}
	for i := range k.Experience {
		if file, ok := byPath[k.Experience[i].Source.Path]; ok {
			annotateSource(&k.Experience[i].Source, file)
		}
	}
	for i := range k.Encounters {
		if file, ok := byPath[k.Encounters[i].Source.Path]; ok {
			annotateSource(&k.Encounters[i].Source, file)
		}
	}
	for i := range k.EnemyBases {
		if file, ok := byPath[k.EnemyBases[i].Source.Path]; ok {
			annotateSource(&k.EnemyBases[i].Source, file)
		}
	}
	for i := range k.EnemiesTable {
		if file, ok := byPath[k.EnemiesTable[i].Source.Path]; ok {
			annotateSource(&k.EnemiesTable[i].Source, file)
		}
	}
	for i := range k.Groups {
		if file, ok := byPath[k.Groups[i].Source.Path]; ok {
			annotateSource(&k.Groups[i].Source, file)
		}
	}
	for i := range k.Warps {
		if file, ok := byPath[k.Warps[i].Source.Path]; ok {
			annotateSource(&k.Warps[i].Source, file)
		}
	}
	for i := range k.Leveling {
		for j := range k.Leveling[i].Evidence {
			if file, ok := byPath[k.Leveling[i].Evidence[j].Path]; ok {
				annotateSource(&k.Leveling[i].Evidence[j], file)
			}
		}
	}
}
