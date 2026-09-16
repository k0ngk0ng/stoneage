package aimcp

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	maxSkillFileBytes = 512 * 1024
	maxSkillTreeBytes = 4 * 1024 * 1024
)

// SkillSpec is an allowlisted product skill.  RelativePath is resolved only
// below the installer root and SHA256 covers every regular file in that
// directory (path and bytes), in deterministic order.
type SkillSpec struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	RelativePath string `json:"relative_path"`
	SHA256       string `json:"sha256"`
}

// The hashes are filled from the repository's checked-in skill documents.
// Keeping the catalog in Go makes "install" an allowlisted operation rather
// than a request to discover/download arbitrary model skills.
var builtinSkillCatalog = map[string]SkillSpec{
	"stoneage-play": {
		Name: "stoneage-play", Version: "1.2.0", RelativePath: "stoneage-play",
		SHA256: "cdd451b421089fd74dfd15d7c563e669eb757d42361c792132bec1b204f7f07f",
	},
	"stoneage-quest": {
		Name: "stoneage-quest", Version: "1.2.0", RelativePath: "stoneage-quest",
		SHA256: "de733709ae679274d229f929f2b48db7cc9284a61372327506dd57ebbe3dd931",
	},
	"stoneage-leveling": {
		Name: "stoneage-leveling", Version: "1.2.0", RelativePath: "stoneage-leveling",
		SHA256: "efa295cb1e767ed92de8476d583407d50e10a93e74aedf175c82f792e7895bab",
	},
	"stoneage-social": {
		Name: "stoneage-social", Version: "1.2.0", RelativePath: "stoneage-social",
		SHA256: "40bc4fc7a1571574155de4cd00e1182fff364dc29854ae5029fdc94dd5d80776",
	},
}

// legacySkillTreeHashes contains the only older installations that may be
// replaced automatically when the catalog advances.  This is deliberately
// separate from builtinSkillCatalog: changing the current catalog must not
// implicitly make an arbitrary historical or user-authored tree eligible for
// replacement.
var legacySkillTreeHashes = map[string]string{
	"stoneage-play":   "f0bc11b4ca6af46e8f4b4673d522695303a5080b6afa5a5ad43ed85f44f731d7",
	"stoneage-social": "70e8b3b297754c9b78eee137b0c2f03f7189381beaec71058866c4b9f31bfb42",
}

// SkillCatalog returns a copy of the fixed catalog for management displays.
func SkillCatalog() []SkillSpec {
	result := make([]SkillSpec, 0, len(builtinSkillCatalog))
	for _, spec := range builtinSkillCatalog {
		result = append(result, spec)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

type SkillInstaller struct {
	root string
}

// NewSkillInstaller does not inspect a caller-provided catalog.  The root
// only selects where the checked-in, hash-pinned catalog is read from.
func NewSkillInstaller(root string) (*SkillInstaller, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" || root == "." {
		return nil, fmt.Errorf("%w: skill root is required", ErrSkillPath)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, fmt.Errorf("%w: skill root unavailable", ErrSkillPath)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("%w: skill root must be a directory", ErrSkillPath)
	}
	if err := rejectAnySymlinkComponents(root); err != nil {
		return nil, err
	}
	if err := rejectSymlinkComponents(root, root); err != nil {
		return nil, err
	}
	return &SkillInstaller{root: root}, nil
}

func (installer *SkillInstaller) Specification(name string) (SkillSpec, error) {
	spec, ok := builtinSkillCatalog[name]
	if !ok {
		return SkillSpec{}, ErrSkillNotFound
	}
	return spec, nil
}

// Verify validates the fixed catalog entry and returns its specification. It
// does not write anything.
func (installer *SkillInstaller) Verify(name string) (SkillSpec, error) {
	if installer == nil {
		return SkillSpec{}, ErrSkillPath
	}
	spec, err := installer.Specification(name)
	if err != nil {
		return SkillSpec{}, err
	}
	source, err := installer.sourcePath(spec)
	if err != nil {
		return SkillSpec{}, err
	}
	if err := validateSkillDocument(source, spec); err != nil {
		return SkillSpec{}, err
	}
	hash, err := hashSkillTree(source)
	if err != nil {
		return SkillSpec{}, fmt.Errorf("%w: cannot hash %s", ErrSkillIntegrity, spec.Name)
	}
	if !strings.EqualFold(hash, spec.SHA256) {
		return SkillSpec{}, fmt.Errorf("%w: %s", ErrSkillIntegrity, spec.Name)
	}
	return spec, nil
}

// Install copies a verified fixed skill to <agentWorkdir>/.agents/skills/name.
// The destination is never followed through symlink components. Existing
// matching installations are reused. A complete, hash-pinned tree from the
// legacy allowlist is upgraded transactionally; every other existing tree is
// a conflict and is not overwritten.
func (installer *SkillInstaller) Install(name, agentWorkdir string) (SkillSpec, error) {
	workdir, err := prepareWorkdir(agentWorkdir)
	if err != nil {
		return SkillSpec{}, err
	}
	spec, err := installer.Verify(name)
	if err != nil {
		return SkillSpec{}, err
	}
	agentsDir := filepath.Join(workdir, ".agents")
	if err := ensureDirectoryNoSymlink(agentsDir, workdir); err != nil {
		return SkillSpec{}, err
	}
	skillsDir := filepath.Join(agentsDir, "skills")
	if err := ensureDirectoryNoSymlink(skillsDir, workdir); err != nil {
		return SkillSpec{}, err
	}
	target := filepath.Join(skillsDir, spec.Name)
	if err := ensureInside(workdir, target); err != nil {
		return SkillSpec{}, err
	}
	if info, statErr := os.Lstat(target); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return SkillSpec{}, fmt.Errorf("%w: destination is not a directory", ErrSkillPath)
		}
		hash, hashErr := hashSkillTree(target)
		if hashErr == nil && strings.EqualFold(hash, spec.SHA256) {
			return spec, nil
		}
		if hashErr == nil && isLegacySkillTree(spec.Name, hash) {
			if err := installer.upgrade(target, skillsDir, spec); err != nil {
				return SkillSpec{}, err
			}
			return spec, nil
		}
		return SkillSpec{}, fmt.Errorf("%w: %s", ErrSkillConflict, spec.Name)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return SkillSpec{}, fmt.Errorf("%w: inspect destination", ErrSkillPath)
	}

	temporary, err := os.MkdirTemp(skillsDir, ".stoneage-skill-install-")
	if err != nil {
		return SkillSpec{}, fmt.Errorf("%w: create temporary destination", ErrSkillPath)
	}
	keepTemporary := false
	defer func() {
		if !keepTemporary {
			_ = os.RemoveAll(temporary)
		}
	}()
	source, err := installer.sourcePath(spec)
	if err != nil {
		return SkillSpec{}, err
	}
	if err := copySkillTree(source, temporary); err != nil {
		return SkillSpec{}, err
	}
	if err := validateSkillDocument(temporary, spec); err != nil {
		return SkillSpec{}, err
	}
	installedHash, err := hashSkillTree(temporary)
	if err != nil || !strings.EqualFold(installedHash, spec.SHA256) {
		return SkillSpec{}, fmt.Errorf("%w: copied %s changed during installation", ErrSkillIntegrity, spec.Name)
	}
	if err := os.Rename(temporary, target); err != nil {
		if errors.Is(err, os.ErrExist) {
			return SkillSpec{}, fmt.Errorf("%w: %s", ErrSkillConflict, spec.Name)
		}
		return SkillSpec{}, fmt.Errorf("%w: publish installation", ErrSkillPath)
	}
	keepTemporary = true
	return spec, nil
}

// upgrade replaces a known legacy tree with a freshly copied and validated
// catalog tree. The old directory is first moved aside on the same
// filesystem, so a failed publish can restore it with a single rename.
func (installer *SkillInstaller) upgrade(target, skillsDir string, spec SkillSpec) error {
	source, err := installer.sourcePath(spec)
	if err != nil {
		return err
	}
	temporary, err := os.MkdirTemp(skillsDir, ".stoneage-skill-upgrade-")
	if err != nil {
		return fmt.Errorf("%w: create temporary upgrade", ErrSkillPath)
	}
	keepTemporary := false
	defer func() {
		if !keepTemporary {
			_ = os.RemoveAll(temporary)
		}
	}()

	// Build and validate the complete replacement before touching the old
	// installation. Verify was already called by Install, but the source may
	// have changed between that call and this copy.
	if err := copySkillTree(source, temporary); err != nil {
		return err
	}
	if err := validateSkillDocument(temporary, spec); err != nil {
		return err
	}
	installedHash, err := hashSkillTree(temporary)
	if err != nil || !strings.EqualFold(installedHash, spec.SHA256) {
		return fmt.Errorf("%w: copied %s changed during upgrade", ErrSkillIntegrity, spec.Name)
	}

	// Recheck the complete old tree immediately before moving it. This avoids
	// replacing a legacy installation that was edited after Install inspected
	// it.
	if err := verifyLegacyDestination(target, spec.Name); err != nil {
		return err
	}
	backup, err := reserveSiblingDirectory(skillsDir, ".stoneage-skill-backup-")
	if err != nil {
		return fmt.Errorf("%w: reserve upgrade backup", ErrSkillPath)
	}
	backupExists := false
	defer func() {
		if backupExists {
			// A backup is retained if publishing or restoration fails. It is
			// the intact recovery copy and must not be silently discarded.
			return
		}
		_ = os.RemoveAll(backup)
	}()

	if err := os.Rename(target, backup); err != nil {
		return fmt.Errorf("%w: move legacy installation aside", ErrSkillPath)
	}
	backupExists = true
	if err := os.Rename(temporary, target); err != nil {
		// The old tree is still at backup. Restore it before returning, and
		// retain the backup if restoration itself fails for diagnosis/recovery.
		if restoreErr := os.Rename(backup, target); restoreErr != nil {
			return fmt.Errorf("%w: publish upgrade (%v); restore legacy installation: %v", ErrSkillPath, err, restoreErr)
		}
		backupExists = false
		return fmt.Errorf("%w: publish upgrade", ErrSkillPath)
	}
	keepTemporary = true
	if err := removeReservedBackup(backup); err != nil {
		// The new target is valid. Returning an error while retaining an
		// intact old backup gives an operator a safe recovery point if cleanup
		// is blocked by the filesystem.
		return fmt.Errorf("%w: remove upgrade backup", ErrSkillPath)
	}
	backupExists = false
	return nil
}

func isLegacySkillTree(name, hash string) bool {
	legacyHash, ok := legacySkillTreeHashes[name]
	return ok && strings.EqualFold(hash, legacyHash)
}

func verifyLegacyDestination(target, name string) error {
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: legacy destination disappeared", ErrSkillConflict)
	}
	if err != nil {
		return fmt.Errorf("%w: inspect legacy destination", ErrSkillPath)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: legacy destination is not a directory", ErrSkillPath)
	}
	hash, err := hashSkillTree(target)
	if err != nil || !isLegacySkillTree(name, hash) {
		return fmt.Errorf("%w: legacy destination changed", ErrSkillConflict)
	}
	return nil
}

// reserveSiblingDirectory returns a path that does not exist while ensuring
// the random suffix comes from the target directory. The caller owns that
// path and must either publish it or remove it.
func reserveSiblingDirectory(parent, prefix string) (string, error) {
	reserved, err := os.MkdirTemp(parent, prefix)
	if err != nil {
		return "", err
	}
	if err := os.Remove(reserved); err != nil {
		_ = os.RemoveAll(reserved)
		return "", err
	}
	return reserved, nil
}

func removeReservedBackup(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("backup is not a directory")
	}
	return os.RemoveAll(path)
}

// Reconcile updates the catalog-managed skills for a stopped profile. Removed
// selections must not remain discoverable in its persistent workspace. Only
// intact, hash-pinned catalog trees are removed; unexpected content is a
// conflict requiring review and is never silently deleted.
func (installer *SkillInstaller) Reconcile(names []string, agentWorkdir string) error {
	workdir, err := prepareWorkdir(agentWorkdir)
	if err != nil {
		return err
	}
	selected := make(map[string]bool, len(names))
	for _, name := range names {
		if _, err := installer.Verify(name); err != nil {
			return err
		}
		selected[name] = true
	}
	skillsDir := filepath.Join(workdir, ".agents", "skills")
	if err := rejectSymlinkComponents(skillsDir, workdir); err != nil {
		return err
	}
	var remove []string
	for _, spec := range SkillCatalog() {
		if selected[spec.Name] {
			continue
		}
		target := filepath.Join(skillsDir, spec.Name)
		info, err := os.Lstat(target)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return ErrSkillPath
		}
		hash, err := hashSkillTree(target)
		if err != nil || (!strings.EqualFold(hash, spec.SHA256) && !isLegacySkillTree(spec.Name, hash)) {
			return fmt.Errorf("%w: %s", ErrSkillConflict, spec.Name)
		}
		remove = append(remove, target)
	}
	for _, name := range names {
		if _, err := installer.Install(name, workdir); err != nil {
			return err
		}
	}
	for _, target := range remove {
		if err := rejectSymlinkComponents(target, workdir); err != nil {
			return err
		}
		if err := os.RemoveAll(target); err != nil {
			return fmt.Errorf("%w: remove deselected catalog skill", ErrSkillPath)
		}
	}
	return nil
}

func (installer *SkillInstaller) sourcePath(spec SkillSpec) (string, error) {
	if installer == nil || spec.Name == "" || spec.RelativePath == "" || filepath.IsAbs(spec.RelativePath) {
		return "", ErrSkillPath
	}
	path := filepath.Join(installer.root, filepath.Clean(spec.RelativePath))
	if err := ensureInside(installer.root, path); err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("%w: catalog entry is unavailable", ErrSkillIntegrity)
	}
	if err := rejectSymlinkComponents(path, installer.root); err != nil {
		return "", err
	}
	return path, nil
}

func prepareWorkdir(path string) (string, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" || path == "." || filepath.IsAbs(path) == false {
		return "", fmt.Errorf("%w: agent workdir must be an existing absolute path", ErrSkillPath)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("%w: agent workdir unavailable", ErrSkillPath)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("%w: agent workdir must be a directory", ErrSkillPath)
	}
	// The caller's workdir may be reached through an operating-system path
	// alias (for example, macOS's /var -> /private/var).  Ancestors outside
	// the workdir are therefore allowed; all components created below the
	// workdir are checked by ensureDirectoryNoSymlink.
	if err := rejectSymlinkComponents(path, path); err != nil {
		return "", err
	}
	return path, nil
}

func ensureDirectoryNoSymlink(path, root string) error {
	if err := ensureInside(root, path); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%w: destination component is unsafe", ErrSkillPath)
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(path, 0755); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: create destination component", ErrSkillPath)
		}
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%w: destination component is unsafe", ErrSkillPath)
		}
	} else {
		return fmt.Errorf("%w: inspect destination component", ErrSkillPath)
	}
	return rejectSymlinkComponents(path, root)
}

func ensureInside(root, path string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("%w: path escapes allowed root", ErrSkillPath)
	}
	return nil
}

func rejectSymlinkComponents(path, root string) error {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	if err := ensureInside(root, path); err != nil {
		return err
	}
	rel, _ := filepath.Rel(root, path)
	current := root
	if rel == "." {
		return nil
	}
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("%w: inspect path component", ErrSkillPath)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: symlinks are not allowed", ErrSkillPath)
		}
	}
	return nil
}

func rejectAnySymlinkComponents(path string) error {
	path = filepath.Clean(path)
	for {
		info, err := os.Lstat(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("%w: inspect path ancestor", ErrSkillPath)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: symlinks are not allowed", ErrSkillPath)
		}
		parent := filepath.Dir(path)
		if parent == path {
			return nil
		}
		path = parent
	}
}

func validateSkillDocument(path string, spec SkillSpec) error {
	documentPath := filepath.Join(path, "SKILL.md")
	if err := ensureInside(path, documentPath); err != nil {
		return err
	}
	data, err := os.ReadFile(documentPath)
	if err != nil || len(data) == 0 || len(data) > maxSkillFileBytes {
		return fmt.Errorf("%w: SKILL.md is unavailable", ErrSkillIntegrity)
	}
	text := string(data)
	if !strings.HasPrefix(text, "---\n") {
		return fmt.Errorf("%w: missing YAML frontmatter", ErrSkillIntegrity)
	}
	end := strings.Index(text[4:], "\n---\n")
	if end < 0 {
		return fmt.Errorf("%w: malformed YAML frontmatter", ErrSkillIntegrity)
	}
	frontmatter := text[4 : 4+end]
	if !frontmatterHas(frontmatter, "name", spec.Name) || !frontmatterHas(frontmatter, "version", spec.Version) || !frontmatterHas(frontmatter, "description", "") {
		return fmt.Errorf("%w: frontmatter does not match catalog", ErrSkillIntegrity)
	}
	return nil
}

func frontmatterHas(frontmatter, key, expected string) bool {
	for _, line := range strings.Split(frontmatter, "\n") {
		name, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(name) != key {
			continue
		}
		value = strings.TrimSpace(value)
		if key == "description" {
			return value != ""
		}
		return strings.Trim(value, "\"'") == expected
	}
	return false
}

func hashSkillTree(root string) (string, error) {
	entries := make([]string, 0)
	var total int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: symlink in skill tree", ErrSkillIntegrity)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("%w: non-regular skill file", ErrSkillIntegrity)
		}
		if info.Size() < 0 || info.Size() > maxSkillFileBytes {
			return fmt.Errorf("%w: skill file is too large", ErrSkillIntegrity)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entries = append(entries, rel)
		total += info.Size()
		if total > maxSkillTreeBytes {
			return fmt.Errorf("%w: skill tree is too large", ErrSkillIntegrity)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(entries)
	hash := sha256.New()
	for _, rel := range entries {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			return "", err
		}
		_, _ = io.WriteString(hash, filepath.ToSlash(rel))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(data)
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func copySkillTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: symlink in skill tree", ErrSkillIntegrity)
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := destination
		if rel != "." {
			target = filepath.Join(destination, rel)
			if err := ensureInside(destination, target); err != nil {
				return err
			}
		}
		if entry.IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return fmt.Errorf("%w: copy skill directory", ErrSkillPath)
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("%w: non-regular skill file", ErrSkillIntegrity)
		}
		if info.Size() > maxSkillFileBytes {
			return fmt.Errorf("%w: skill file is too large", ErrSkillIntegrity)
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			_ = input.Close()
			return fmt.Errorf("%w: copy skill file", ErrSkillPath)
		}
		copied, copyErr := io.CopyN(output, input, info.Size()+1)
		inputCloseErr := input.Close()
		closeErr := output.Close()
		if copyErr == nil {
			// CopyN intentionally reads one byte beyond the declared size to
			// detect a file changing while it is copied.
			return fmt.Errorf("%w: skill file changed during copy", ErrSkillIntegrity)
		}
		if !errors.Is(copyErr, io.EOF) || copied != info.Size() || inputCloseErr != nil || closeErr != nil {
			return fmt.Errorf("%w: copy skill file", ErrSkillPath)
		}
		return nil
	})
}
