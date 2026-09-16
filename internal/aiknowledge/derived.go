package aiknowledge

import (
	"fmt"
	"sort"
)

func validateReferences(k *Knowledge, strict bool) error {
	baseByID := make(map[int]EnemyBase, len(k.EnemyBases))
	for _, base := range k.EnemyBases {
		baseByID[base.TemplateID] = base
	}
	enemyByID := make(map[int]Enemy, len(k.EnemiesTable))
	for _, enemy := range k.EnemiesTable {
		enemyByID[enemy.ID] = enemy
		if _, ok := baseByID[enemy.TemplateID]; !ok {
			if err := addReferenceIssue(k, strict, enemy.Source, "enemy_unknown_template", fmt.Sprintf("enemy %d references missing enemybase template %d", enemy.ID, enemy.TemplateID)); err != nil {
				return err
			}
		}
	}
	groupByID := make(map[int]EncounterGroup, len(k.Groups))
	for _, group := range k.Groups {
		groupByID[group.ID] = group
		for _, enemyID := range group.EnemyIDs {
			if enemyID < 0 {
				continue
			}
			if _, ok := enemyByID[enemyID]; !ok {
				if err := addReferenceIssue(k, strict, group.Source, "group_unknown_enemy", fmt.Sprintf("group %d references missing enemy %d", group.ID, enemyID)); err != nil {
					return err
				}
			}
		}
	}
	for _, area := range k.Encounters {
		for _, groupID := range area.GroupIDs {
			if groupID < 0 {
				continue
			}
			if _, ok := groupByID[groupID]; !ok {
				if err := addReferenceIssue(k, strict, area.Source, "encount_unknown_group", fmt.Sprintf("encounter %d references missing group %d", area.ID, groupID)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func addReferenceIssue(k *Knowledge, strict bool, source SourceRef, code, message string) error {
	severity := SeverityWarning
	if strict {
		severity = SeverityError
	}
	sourceCopy := source
	k.Issues = append(k.Issues, Issue{Severity: severity, Code: code, Message: message, Source: &sourceCopy})
	if strict {
		return fmt.Errorf("%s: %s", code, message)
	}
	return nil
}

func deriveLevelingAreas(k *Knowledge) []LevelingArea {
	groups := make(map[int]EncounterGroup, len(k.Groups))
	for _, group := range k.Groups {
		groups[group.ID] = group
	}
	enemies := make(map[int]Enemy, len(k.EnemiesTable))
	bases := make(map[int]EnemyBase, len(k.EnemyBases))
	for _, enemy := range k.EnemiesTable {
		enemies[enemy.ID] = enemy
	}
	for _, base := range k.EnemyBases {
		bases[base.TemplateID] = base
	}
	areas := make([]LevelingArea, 0, len(k.Encounters))
	for _, encounter := range k.Encounters {
		area := LevelingArea{
			ID: encounter.ID, Floor: encounter.Floor, Bounds: encounter.Bounds,
			EncounterIDs: []int{encounter.ID}, GroupIDs: make([]int, 0), EnemyIDs: make([]int, 0), TemplateIDs: make([]int, 0),
			Levels: Range{Min: 0, Max: 0}, EncounterProbability: encounter.EncounterProbability, MaxEnemies: encounter.MaxEnemies,
			Verified: true, Evidence: []SourceRef{encounter.Source},
		}
		seenGroups := map[int]bool{}
		seenEnemies := map[int]bool{}
		seenBases := map[int]bool{}
		levelSet := false
		for _, groupID := range encounter.GroupIDs {
			if groupID < 0 || seenGroups[groupID] {
				continue
			}
			seenGroups[groupID] = true
			area.GroupIDs = append(area.GroupIDs, groupID)
			group, ok := groups[groupID]
			if !ok {
				area.Verified = false
				continue
			}
			area.Evidence = append(area.Evidence, group.Source)
			for _, enemyID := range group.EnemyIDs {
				if enemyID < 0 || seenEnemies[enemyID] {
					continue
				}
				seenEnemies[enemyID] = true
				area.EnemyIDs = append(area.EnemyIDs, enemyID)
				enemy, ok := enemies[enemyID]
				if !ok {
					area.Verified = false
					continue
				}
				area.Evidence = append(area.Evidence, enemy.Source)
				if !levelSet {
					area.Levels = enemy.Levels
					levelSet = true
				} else {
					if enemy.Levels.Min < area.Levels.Min {
						area.Levels.Min = enemy.Levels.Min
					}
					if enemy.Levels.Max > area.Levels.Max {
						area.Levels.Max = enemy.Levels.Max
					}
				}
				if !seenBases[enemy.TemplateID] {
					seenBases[enemy.TemplateID] = true
					if base, ok := bases[enemy.TemplateID]; ok {
						area.TemplateIDs = append(area.TemplateIDs, enemy.TemplateID)
						area.Evidence = append(area.Evidence, base.Source)
					} else {
						area.Verified = false
					}
				}
			}
		}
		if len(area.GroupIDs) == 0 || len(area.EnemyIDs) == 0 {
			area.Verified = false
		}
		area.Evidence = uniqueSources(area.Evidence)
		sort.Ints(area.GroupIDs)
		sort.Ints(area.EnemyIDs)
		sort.Ints(area.TemplateIDs)
		areas = append(areas, area)
	}
	sort.SliceStable(areas, func(i, j int) bool {
		if areas[i].Floor != areas[j].Floor {
			return areas[i].Floor < areas[j].Floor
		}
		return areas[i].ID < areas[j].ID
	})
	return areas
}

func uniqueSources(input []SourceRef) []SourceRef {
	seen := make(map[string]bool, len(input))
	result := make([]SourceRef, 0, len(input))
	for _, source := range input {
		key := fmt.Sprintf("%s:%d:%d", source.Path, source.Line, source.LineEnd)
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, source)
	}
	return result
}

func makeCoverage(k *Knowledge) Coverage {
	coverage := Coverage{CoreTables: map[string]bool{}}
	for _, file := range coreDataFiles {
		coverage.CoreTables[file] = false
	}
	for _, file := range k.Files {
		if _, ok := coverage.CoreTables[file.Path]; ok {
			coverage.CoreTables[file.Path] = file.Supported
		}
	}
	coverage.NPCFiles = len(k.NPC.Files)
	for _, file := range k.NPC.Files {
		if file.Supported {
			coverage.SupportedNPCFiles++
		}
	}
	coverage.TasksTotal = len(k.TaskDefinitions)
	for _, task := range k.TaskDefinitions {
		switch task.Status {
		case TaskVerified:
			coverage.TasksVerified++
		case TaskUnsupported:
			coverage.TasksUnsupported++
		default:
			coverage.TasksUnverified++
		}
	}
	coverage.LevelingAreas = len(k.Leveling)
	for path, ok := range coverage.CoreTables {
		if !ok {
			coverage.Gaps = append(coverage.Gaps, "core table unavailable: "+path)
		}
	}
	for _, file := range k.NPC.Files {
		if !file.Supported {
			coverage.Gaps = append(coverage.Gaps, "unsupported NPC artefact: "+file.Path)
		}
	}
	for _, area := range k.Leveling {
		if !area.Verified {
			coverage.Gaps = append(coverage.Gaps, fmt.Sprintf("unverified leveling area: %d", area.ID))
		}
	}
	if coverage.TasksUnverified > 0 || coverage.TasksUnsupported > 0 {
		coverage.Gaps = append(coverage.Gaps, "task definitions require manual verification")
	}
	sort.Strings(coverage.Gaps)
	coverage.Complete = len(coverage.Gaps) == 0 && len(k.Issues) == 0
	return coverage
}
