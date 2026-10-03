package aiknowledge

import (
	"fmt"
	"path"
	"strings"
)

// NPCEventBinding connects an event script to its actual creation record and
// handler. This is a source fact, never a runtime execution approval.
type NPCEventBinding struct {
	Create      SourceRef   `json:"create"`
	Templates   []SourceRef `json:"templates"`
	Script      SourceRef   `json:"script"`
	Template    string      `json:"template"`
	Name        string      `json:"name"`
	FunctionSet string      `json:"function_set,omitempty"`
	Floor       int         `json:"floor"`
	Born        Rectangle   `json:"born"`
	Move        Rectangle   `json:"move"`
	SpawnCount  int         `json:"spawn_count"`
	Rules       int         `json:"rules"`
	Blockers    []string    `json:"blockers"`
}

// EventBindings keeps every creation record and ambiguity visible. It
// never picks a duplicate template or assumes a moving NPC stays at its spawn.
func (npc NPCKnowledge) EventBindings() []NPCEventBinding {
	templates := map[string][]NPCTemplate{}
	files := map[string]NPCFile{}
	for _, template := range npc.Templates {
		templates[template.Name] = append(templates[template.Name], template)
	}
	for _, file := range npc.Files {
		files[file.Path] = file
	}
	withDigest := func(source SourceRef) SourceRef {
		if file, ok := files[source.Path]; ok {
			source.SHA256 = file.SHA256
		}
		return source
	}
	result := []NPCEventBinding{}
	for _, create := range npc.Creates {
		for _, ref := range create.Enemies {
			scriptPath, pathErr := npcArgumentFile(ref.Argument)
			file, hasFile := files[scriptPath]
			candidates := templates[ref.Template]
			isEvent := hasFile && file.Kind == "event"
			for _, template := range candidates {
				isEvent = isEvent || template.FunctionSet == "ExChangeMan"
			}
			if !isEvent {
				continue
			}
			binding := NPCEventBinding{Create: withDigest(create.Source), Template: ref.Template, Name: create.Name, Floor: create.Floor, Born: create.Born, Move: create.Move, SpawnCount: create.SpawnCount, Script: SourceRef{Path: scriptPath}, Blockers: []string{}}
			for _, template := range candidates {
				binding.Templates = append(binding.Templates, withDigest(template.Source))
			}
			if len(candidates) != 1 {
				binding.Blockers = append(binding.Blockers, fmt.Sprintf("template %q has %d definitions", ref.Template, len(candidates)))
			} else {
				binding.FunctionSet = candidates[0].FunctionSet
				if binding.Name == "" {
					binding.Name = candidates[0].DisplayName
				}
				if binding.FunctionSet != "ExChangeMan" {
					binding.Blockers = append(binding.Blockers, "event source is attached to a different NPC handler")
				}
			}
			if pathErr != nil {
				binding.Blockers = append(binding.Blockers, pathErr.Error())
			}
			if !hasFile {
				binding.Blockers = append(binding.Blockers, "referenced event source is absent")
			} else {
				binding.Script = file.Source
				if file.Events == nil || !file.Supported {
					binding.Blockers = append(binding.Blockers, "event source is not parsed: "+file.Issue)
				} else {
					binding.Rules = len(file.Events.Rules)
					for _, rule := range file.Events.Rules {
						if rule.CommentedControl {
							binding.Blockers = append(binding.Blockers, "commented control fields require native semantic review")
							break
						}
					}
				}
			}
			if create.SpawnCount != 1 || len(create.Enemies) != 1 {
				binding.Blockers = append(binding.Blockers, "spawn does not identify one NPC")
			}
			if create.Born.X != create.Born.X2 || create.Born.Y != create.Born.Y2 || create.Move != create.Born {
				binding.Blockers = append(binding.Blockers, "NPC location is not a fixed single tile")
			}
			if create.Time != 0 || create.Date != 0 || create.Family != 0 {
				binding.Blockers = append(binding.Blockers, "spawn has time, date or family conditions")
			}
			result = append(result, binding)
		}
	}
	return result
}

func npcArgumentFile(argument string) (string, error) {
	for _, token := range strings.Split(argument, "|") {
		// Native NPC_Util_CheckAssignArgFile uses substring "file", then
		// the second colon field in 64-byte buffers. Noncanonical forms stay
		// blocked instead of silently resolving to a different source.
		if !strings.Contains(token, "file") {
			continue
		}
		if !strings.HasPrefix(token, "file:") || len(token) > 63 || strings.Count(token, ":") != 1 {
			return "", fmt.Errorf("ambiguous native file argument %q", token)
		}
		name := strings.TrimPrefix(token, "file:")
		if name == "" || strings.TrimSpace(name) != name || strings.ContainsAny(name, "\\\x00\r\n") || path.IsAbs(name) || path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") {
			return "", fmt.Errorf("unsupported NPC file path %q", name)
		}
		return "npc/" + name, nil
	}
	return "", fmt.Errorf("NPC has no supported file argument")
}
