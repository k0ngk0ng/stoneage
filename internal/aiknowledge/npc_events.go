package aiknowledge

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// NPCEventScript describes the ordered source of an ExChangeMan conversation.
// Parsed is not execution-verified: handlers, routes, runtime windows and
// effects must still be checked before compiling an executable task.
type NPCEventScript struct {
	Defaults []NPCEventField `json:"defaults,omitempty"`
	Rules    []NPCEventRule  `json:"rules"`
}

type NPCEventField struct {
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	Source    SourceRef `json:"source"`
	Commented bool      `json:"commented,omitempty"`
	// Bare distinguishes controls such as Break (and uninterpreted prose)
	// from key:value fields. Such lines are not continuations of a message.
	Bare bool `json:"bare,omitempty"`
}

type NPCEventRule struct {
	Number     int    `json:"number"`
	Type       string `json:"type"`
	Expression string `json:"expression"`
	// Alternatives preserve EVENT's comma-separated OR groups; each group's
	// ampersand-separated predicates must all hold. Never flatten these.
	Alternatives     [][]string      `json:"alternatives"`
	Fields           []NPCEventField `json:"fields"`
	Source           SourceRef       `json:"source"`
	CommentedControl bool            `json:"commented_control,omitempty"`
}

// Field returns the first exact source key. Duplicate keys remain visible in
// Fields. This is source access, not NPC_Util_GetStrFromStrWithDelim emulation:
// that native helper also matches substrings and has fixed-size buffers.
func (r NPCEventRule) Field(key string) (string, bool) {
	for _, f := range r.Fields {
		if f.Key == key {
			return f.Value, true
		}
	}
	return "", false
}

// Identify the source dialect from its content, not its extension or name.
func isNPCEventScript(raw []byte) bool {
	return strings.Contains(string(raw), "EventNo:")
}

var npcCommentField = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*:`)

func parseNPCEvents(raw []byte, path string) (*NPCEventScript, []Issue, error) {
	lines, issues := decodeLines(raw, false)
	if len(issues) > 0 {
		return nil, issues, fmt.Errorf("%s: event script contains undecodable lines", path)
	}
	result := &NPCEventScript{}
	var rule *NPCEventRule
	var fields []NPCEventField
	digest := SHA256Hex(raw)
	source := func(line int) SourceRef {
		return SourceRef{Path: path, Line: line, SHA256: digest, Extractor: "npc-event"}
	}
	invalid := func(line int, reason string) (*NPCEventScript, []Issue, error) {
		return nil, issues, fmt.Errorf("%s:%d: %s", path, line, reason)
	}
	for _, line := range lines {
		for _, token := range strings.Split(line.Text, "|") {
			token = strings.TrimSpace(token)
			if token == "" {
				continue
			}
			commented := strings.HasPrefix(token, "#")
			if commented {
				// Native chompex only removes line endings. A commented field
				// may still match the native substring lookup; do not silently
				// discard it and claim to know the selected branch.
				remainder := strings.TrimSpace(strings.TrimLeft(token, "#"))
				if npcCommentField.MatchString(remainder) || remainder == "EventEnd" {
					token = remainder
				} else {
					if strings.Contains(token, "EventNo:") || strings.Contains(token, "EventEnd") {
						return invalid(line.Number, "ambiguous native control token in comment")
					}
					continue
				}
			}
			if token == "EventEnd" {
				if rule == nil {
					return invalid(line.Number, "EventEnd without EventNo")
				}
				rule.CommentedControl = rule.CommentedControl || commented
				rule.Fields = fields
				rule.Type, _ = rule.Field("TYPE")
				rule.Expression, _ = rule.Field("EVENT")
				if rule.Type == "" || rule.Expression == "" {
					return invalid(rule.Source.Line, "event requires TYPE and EVENT")
				}
				for _, alternative := range strings.Split(rule.Expression, ",") {
					var terms []string
					for _, term := range strings.Split(alternative, "&") {
						term = strings.TrimSpace(term)
						if term == "" {
							return invalid(rule.Source.Line, "empty EVENT alternative or predicate")
						}
						terms = append(terms, term)
					}
					rule.Alternatives = append(rule.Alternatives, terms)
				}
				rule.Source.LineEnd = line.Number
				result.Rules = append(result.Rules, *rule)
				rule, fields = nil, nil
				continue
			}
			key, value, ok := strings.Cut(token, ":")
			key, value = strings.TrimSpace(key), strings.TrimSpace(value)
			if key == "" || (!ok && (strings.Contains(token, "EventNo") || strings.Contains(token, "EventEnd"))) {
				return invalid(line.Number, "ambiguous event boundary")
			}
			if key == "EventNo" {
				if rule != nil {
					return invalid(line.Number, "EventNo before previous EventEnd")
				}
				number, err := strconv.Atoi(value)
				if err != nil || number < -1 {
					return invalid(line.Number, "invalid EventNo")
				}
				rule = &NPCEventRule{Number: number, Source: source(line.Number)}
			}
			field := NPCEventField{Key: key, Value: value, Source: source(line.Number), Commented: commented, Bare: !ok}
			if rule == nil {
				result.Defaults = append(result.Defaults, field)
			} else {
				rule.CommentedControl = rule.CommentedControl || commented
				fields = append(fields, field)
			}
		}
	}
	if rule != nil {
		return invalid(rule.Source.Line, "event is missing EventEnd")
	}
	if len(result.Rules) == 0 {
		return invalid(1, "no event rules")
	}
	return result, issues, nil
}
