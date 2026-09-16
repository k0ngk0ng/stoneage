package aiknowledge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func loadNPC(ctx context.Context, dataDir string, files []FileDigest, issues []Issue, strict bool) (NPCKnowledge, []FileDigest, []Issue, error) {
	result := NPCKnowledge{Templates: make([]NPCTemplate, 0), Creates: make([]NPCCreate, 0), Files: make([]NPCFile, 0)}
	npcDir := filepath.Join(dataDir, "npc")
	if info, err := os.Stat(npcDir); err != nil || !info.IsDir() {
		issues = append(issues, Issue{Severity: SeverityWarning, Code: "npc_missing", Message: "npc directory is not present; NPC and task coverage is incomplete", Source: &SourceRef{Path: "npc"}})
		return result, files, issues, nil
	}
	var paths []string
	err := filepath.WalkDir(npcDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if contextErr(ctx) != nil {
			return contextErr(ctx)
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(npcDir, path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		if contextErr(ctx) != nil {
			return result, files, issues, contextErr(ctx)
		}
		return result, files, issues, fmt.Errorf("aiknowledge: walk %s: %w", npcDir, err)
	}
	sort.Strings(paths)
	for _, rel := range paths {
		if err := contextErr(ctx); err != nil {
			return result, files, issues, err
		}
		raw, err := os.ReadFile(filepath.Join(npcDir, filepath.FromSlash(rel)))
		if err != nil {
			return result, files, issues, fmt.Errorf("aiknowledge: read npc/%s: %w", rel, err)
		}
		sourcePath := "npc/" + rel
		digest := FileDigest{Path: sourcePath, SHA256: SHA256Hex(raw), Bytes: int64(len(raw)), Encoding: detectEncoding(raw), Supported: true}
		kind := npcKind(rel)
		npcFile := NPCFile{Path: sourcePath, Kind: kind, SHA256: digest.SHA256, Bytes: digest.Bytes, Encoding: digest.Encoding, Supported: true, Source: SourceRef{Path: sourcePath, SHA256: digest.SHA256, Encoding: digest.Encoding, Extractor: "npc"}}
		var parseIssues []Issue
		switch kind {
		case "template":
			var values []NPCTemplate
			values, parseIssues, err = parseNPCTemplates(raw, sourcePath, strict)
			result.Templates = append(result.Templates, values...)
		case "create":
			var values []NPCCreate
			values, parseIssues, err = parseNPCCreates(raw, sourcePath, strict)
			result.Creates = append(result.Creates, values...)
		case "arg", "conf":
			var fields map[string][]string
			fields, parseIssues, err = parseAuxConfig(raw, sourcePath, strict)
			npcFile.Fields = fields
		case "generator":
			npcFile.Supported = false
			npcFile.Issue = "generator or binary artefact is outside the executable NPC configuration parser"
			parseIssues = append(parseIssues, Issue{Severity: SeverityWarning, Code: "npc_unsupported", Message: npcFile.Issue, Source: &npcFile.Source})
		default:
			// Keep unknown files in the digest and make the unsupported boundary
			// visible.  They may contain scripts or generated data that still
			// matter to an operator, but no executor may treat them as facts.
			npcFile.Supported = false
			npcFile.Issue = "file type is not parsed as executable NPC configuration"
			parseIssues = append(parseIssues, Issue{Severity: SeverityWarning, Code: "npc_unsupported", Message: npcFile.Issue, Source: &npcFile.Source})
		}
		if err != nil {
			if strict && kind != "generator" && kind != "other" {
				return result, files, issues, err
			}
			npcFile.Supported = false
			npcFile.Issue = err.Error()
			parseIssues = append(parseIssues, Issue{Severity: SeverityWarning, Code: "npc_parse", Message: err.Error(), Source: &npcFile.Source})
		}
		for i := range parseIssues {
			if parseIssues[i].Source == nil {
				parseIssues[i].Source = &SourceRef{Path: sourcePath}
			} else {
				parseIssues[i].Source.Path = sourcePath
			}
		}
		issues = append(issues, parseIssues...)
		digest.Supported = npcFile.Supported
		digest.Issue = npcFile.Issue
		files = append(files, digest)
		result.Files = append(result.Files, npcFile)
	}
	sort.SliceStable(result.Templates, func(i, j int) bool {
		if result.Templates[i].Name != result.Templates[j].Name {
			return result.Templates[i].Name < result.Templates[j].Name
		}
		return result.Templates[i].Source.Path < result.Templates[j].Source.Path
	})
	sort.SliceStable(result.Creates, func(i, j int) bool {
		if result.Creates[i].Floor != result.Creates[j].Floor {
			return result.Creates[i].Floor < result.Creates[j].Floor
		}
		if result.Creates[i].Source.Path != result.Creates[j].Source.Path {
			return result.Creates[i].Source.Path < result.Creates[j].Source.Path
		}
		return result.Creates[i].Source.Line < result.Creates[j].Source.Line
	})
	sort.SliceStable(result.Files, func(i, j int) bool { return result.Files[i].Path < result.Files[j].Path })
	sort.SliceStable(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	if err := validateNPCReferences(&result, strict, &issues); err != nil {
		return result, files, issues, err
	}
	return result, files, issues, nil
}

func npcKind(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".template", ".templete":
		return "template"
	case ".create":
		return "create"
	case ".arg":
		return "arg"
	case ".conf":
		return "conf"
	case ".gen":
		return "generator"
	default:
		return "other"
	}
}

func parseNPCTemplates(raw []byte, path string, strict bool) ([]NPCTemplate, []Issue, error) {
	lines, issues := decodeLines(raw, true)
	if len(lines) == 0 {
		return nil, issues, fmt.Errorf("%s: empty or undecodable NPC template", path)
	}
	first := strings.TrimSpace(lines[0].Text)
	if first != "NPCTEMPLATE" {
		// The server ignores files whose magic is not the first line.  Treat
		// this as an unsupported artefact, rather than inventing templates from
		// a commented-out old file.
		return nil, issues, fmt.Errorf("%s: missing NPCTEMPLATE header", path)
	}
	blocks, blockIssues, err := parseKVBlocks(lines[1:], path, "template", strict)
	issues = append(issues, blockIssues...)
	if err != nil {
		return nil, issues, err
	}
	result := make([]NPCTemplate, 0, len(blocks))
	for _, block := range blocks {
		name := strings.TrimSpace(block.Fields["templatename"])
		if name == "" {
			if parseErr := recordParseIssue(&issues, strict, path, block.Start, "npc_template_name", "templatename is required"); parseErr != nil {
				return nil, issues, parseErr
			}
			continue
		}
		fields := cloneMap(block.Fields)
		result = append(result, NPCTemplate{Name: name, DisplayName: fields["name"], Type: fields["type"], Graphic: fields["graphicname"], FunctionSet: fields["functionset"], Fields: fields, Source: SourceRef{Path: path, Line: block.Start, LineEnd: block.End, Extractor: "NPCTEMPLATE"}})
	}
	if len(result) == 0 {
		return result, issues, fmt.Errorf("%s: no active NPC template blocks", path)
	}
	return result, issues, nil
}

func parseNPCCreates(raw []byte, path string, strict bool) ([]NPCCreate, []Issue, error) {
	lines, issues := decodeLines(raw, true)
	if len(lines) == 0 {
		return nil, issues, fmt.Errorf("%s: empty or undecodable NPCCREATE", path)
	}
	if strings.TrimSpace(lines[0].Text) != "NPCCREATE" {
		return nil, issues, fmt.Errorf("%s: missing NPCCREATE header", path)
	}
	blocks, blockIssues, err := parseKVBlocks(lines[1:], path, "create", strict)
	issues = append(issues, blockIssues...)
	if err != nil {
		return nil, issues, err
	}
	result := make([]NPCCreate, 0, len(blocks))
	for _, block := range blocks {
		fields := cloneMap(block.Fields)
		floor, err := strconv.Atoi(strings.TrimSpace(fields["floorid"]))
		if err != nil || floor < 0 {
			if parseErr := recordParseIssue(&issues, strict, path, block.Start, "npc_create_floor", fmt.Sprintf("invalid floorid %q", fields["floorid"])); parseErr != nil {
				return nil, issues, parseErr
			}
			continue
		}
		born, bornOK, err := parseNPCBounds(fields, "born")
		if err != nil || !bornOK {
			message := "borncenter or borncorner is required"
			if err != nil {
				message = err.Error()
			}
			if parseErr := recordParseIssue(&issues, strict, path, block.Start, "npc_create_born", message); parseErr != nil {
				return nil, issues, parseErr
			}
			continue
		}
		move, moveOK, err := parseNPCBounds(fields, "move")
		if err != nil {
			if parseErr := recordParseIssue(&issues, strict, path, block.Start, "npc_create_move", err.Error()); parseErr != nil {
				return nil, issues, parseErr
			}
			moveOK = false
		}
		if !moveOK {
			move = born
		}
		direction := parseLooseInt(fields["dir"], 0)
		spawnCount := parseLooseInt(fields["createnum"], 0)
		if spawnCount < 0 {
			spawnCount = 0
		}
		create := NPCCreate{Floor: floor, Born: born, Move: move, Direction: direction, Graphic: fields["graphicname"], Name: fields["name"], SpawnCount: spawnCount, Time: parseLooseInt(fields["time"], 0), Date: parseLooseInt(fields["date"], 0), Family: parseLooseInt(fields["family"], 0), Fields: fields, Source: SourceRef{Path: path, Line: block.Start, LineEnd: block.End, Extractor: "NPCCREATE"}}
		for _, value := range block.Multi["enemy"] {
			parts := strings.SplitN(value, "|", 2)
			ref := NPCEnemyRef{Template: strings.TrimSpace(parts[0])}
			if len(parts) == 2 {
				ref.Argument = strings.TrimSpace(parts[1])
				if colon := strings.IndexByte(ref.Argument, ':'); colon > 0 {
					ref.Kind = strings.ToLower(strings.TrimSpace(ref.Argument[:colon]))
				}
			}
			if ref.Template == "" {
				if parseErr := recordParseIssue(&issues, strict, path, block.Start, "npc_create_enemy", "enemy template is empty"); parseErr != nil {
					return nil, issues, parseErr
				}
				continue
			}
			create.Enemies = append(create.Enemies, ref)
		}
		if len(create.Enemies) == 0 {
			if parseErr := recordParseIssue(&issues, strict, path, block.Start, "npc_create_enemy", "at least one enemy= reference is required"); parseErr != nil {
				return nil, issues, parseErr
			}
			continue
		}
		result = append(result, create)
	}
	if len(result) == 0 {
		return result, issues, fmt.Errorf("%s: no active NPC create blocks", path)
	}
	return result, issues, nil
}

type kvBlock struct {
	Start  int
	End    int
	Fields map[string]string
	Multi  map[string][]string
}

func parseKVBlocks(lines []decodedLine, path, kind string, strict bool) ([]kvBlock, []Issue, error) {
	issues := make([]Issue, 0)
	result := make([]kvBlock, 0)
	var current *kvBlock
	for _, line := range lines {
		text := strings.TrimSpace(line.Text)
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		switch text {
		case "{":
			if current != nil {
				if err := recordParseIssue(&issues, strict, path, line.Number, "npc_nested_block", "nested block is invalid"); err != nil {
					return nil, issues, err
				}
				current = nil
				continue
			}
			current = &kvBlock{Start: line.Number, Fields: map[string]string{}, Multi: map[string][]string{}}
		case "}":
			if current == nil {
				if err := recordParseIssue(&issues, strict, path, line.Number, "npc_unmatched_brace", "closing brace without a block"); err != nil {
					return nil, issues, err
				}
				continue
			}
			current.End = line.Number
			result = append(result, *current)
			current = nil
		default:
			if current == nil {
				// C parser ignores free-form comments and malformed lines outside
				// blocks.  Keep this warning explicit for a task author.
				if strings.Contains(text, "=") {
					if err := recordParseIssue(&issues, strict, path, line.Number, "npc_outside_block", fmt.Sprintf("key outside %s block", kind)); err != nil {
						return nil, issues, err
					}
				}
				continue
			}
			parts := strings.SplitN(text, "=", 2)
			if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
				if err := recordParseIssue(&issues, strict, path, line.Number, "npc_key", "expected key=value inside block"); err != nil {
					return nil, issues, err
				}
				continue
			}
			key := strings.ToLower(strings.TrimSpace(parts[0]))
			value := strings.TrimSpace(parts[1])
			current.Fields[key] = value
			current.Multi[key] = append(current.Multi[key], value)
		}
	}
	if current != nil {
		if err := recordParseIssue(&issues, strict, path, current.Start, "npc_unclosed_block", "block is not closed"); err != nil {
			return nil, issues, err
		}
	}
	return result, issues, nil
}

func parseNPCBounds(fields map[string]string, prefix string) (Rectangle, bool, error) {
	center, hasCenter := fields[prefix+"center"]
	corner, hasCorner := fields[prefix+"corner"]
	if hasCenter == hasCorner {
		if !hasCenter {
			return Rectangle{}, false, nil
		}
		return Rectangle{}, false, fmt.Errorf("%scenter and %scorner cannot both be present", prefix, prefix)
	}
	value := center
	if hasCorner {
		value = corner
	}
	parts := strings.Split(value, ",")
	if len(parts) != 4 {
		return Rectangle{}, false, fmt.Errorf("%s bounds require four integers, got %q", prefix, value)
	}
	ints := [4]int{}
	for i, part := range parts {
		parsed, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return Rectangle{}, false, fmt.Errorf("invalid %s bound %q: %w", prefix, part, err)
		}
		ints[i] = parsed
	}
	if hasCorner {
		return normalizeRectangle(ints[0], ints[1], ints[2], ints[3]), true, nil
	}
	width, height := ints[2], ints[3]
	return normalizeRectangle(ints[0]-width/2, ints[1]-height/2, ints[0]+width/2, ints[1]+height/2), true, nil
}

func parseAuxConfig(raw []byte, path string, strict bool) (map[string][]string, []Issue, error) {
	lines, issues := decodeLines(raw, true)
	fields := map[string][]string{}
	for _, line := range lines {
		text := strings.TrimSpace(line.Text)
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		key, value := "", ""
		if index := strings.IndexAny(text, ":="); index >= 0 {
			key, value = strings.TrimSpace(text[:index]), strings.TrimSpace(text[index+1:])
		} else {
			// Header-like markers (NEWWARPMAN, OVER, etc.) are meaningful
			// controls in the legacy NPC parser, so retain them as empty values.
			key = text
		}
		if key == "" {
			if err := recordParseIssue(&issues, strict, path, line.Number, "npc_aux_key", "empty auxiliary key"); err != nil {
				return nil, issues, err
			}
			continue
		}
		fields[strings.ToLower(key)] = append(fields[strings.ToLower(key)], value)
	}
	return fields, issues, nil
}

func validateNPCReferences(npc *NPCKnowledge, strict bool, issues *[]Issue) error {
	templates := map[string]bool{}
	for _, template := range npc.Templates {
		if templates[template.Name] {
			if err := recordParseIssue(issues, strict, template.Source.Path, template.Source.Line, "npc_duplicate_template", fmt.Sprintf("duplicate template %q", template.Name)); err != nil {
				return err
			}
		}
		templates[template.Name] = true
	}
	for _, create := range npc.Creates {
		for _, ref := range create.Enemies {
			if !templates[ref.Template] {
				if err := recordParseIssue(issues, strict, create.Source.Path, create.Source.Line, "npc_unknown_template", fmt.Sprintf("create references unknown template %q", ref.Template)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func parseLooseInt(value string, fallback int) int {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return fallback
	}
	return parsed
}

func cloneMap(input map[string]string) map[string]string {
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
