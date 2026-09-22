package api

import (
	"encoding/json"
	"regexp"
	"strings"
)

type roleTranscriptObject struct{ start, end int }

var malformedRoleStartPattern = regexp.MustCompile(`(?i)\{\s*["']?thought["']?\s*[:：]`)
var malformedRoleTailPattern = regexp.MustCompile(`(?i),\s*["']?(?:messages|quote_message_id)["']?\s*[:：]`)

// Recover the final protocol object when a provider emits otherwise complete
// role JSON with unescaped quotes. Only the terminal object is considered;
// truncated objects and trailing prose remain blocked.
func recoverMalformedRoleReplyJSON(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	starts := malformedRoleStartPattern.FindAllStringIndex(trimmed, -1)
	if len(starts) == 0 {
		return "", false
	}
	candidate := strings.TrimSpace(trimmed[starts[len(starts)-1][0]:])
	if !strings.HasSuffix(candidate, "}") {
		return "", false
	}
	parsed, ok := parseRoleReply(candidate)
	if !ok {
		return "", false
	}
	parsed.Reply = trimMalformedRoleReplyTail(parsed.Reply)
	parsed.Messages = nil
	parsed.QuoteMessageID = 0
	parsed = normalizeRoleReply(parsed)
	if strings.TrimSpace(parsed.Reply) == "" || suspiciousAssistantDraft(parsed.Reply) {
		return "", false
	}
	encoded, err := json.Marshal(parsed)
	if err != nil {
		return "", false
	}
	return string(encoded), true
}

func trimMalformedRoleReplyTail(reply string) string {
	text := strings.TrimSpace(reply)
	if marker := malformedRoleTailPattern.FindStringIndex(text); marker != nil {
		text = strings.TrimSpace(text[:marker[0]])
	}
	text = strings.TrimSpace(strings.TrimSuffix(text, ","))
	if len(text) >= 2 && ((text[0] == '"' && text[len(text)-1] == '"') || (text[0] == '\'' && text[len(text)-1] == '\'')) {
		text = text[1 : len(text)-1]
	}
	return strings.TrimSpace(decodeLooseJSONEscapes(text))
}

// Recover the terminal role payload from a provider transcript. Earlier role
// drafts, narrative tool markers and result commentary are not user replies.
// Require multiple protocol objects or an explicit transcript marker, and an
// object at the end, so ordinary prose/code containing JSON remains unchanged.
func recoverFinalRoleReplyJSON(raw string) (string, bool) {
	objects, marker := scanRoleTranscript(raw)
	if len(objects) == 0 || (len(objects) < 2 && !marker) {
		return "", false
	}
	last := objects[len(objects)-1]
	if strings.TrimSpace(raw[last.end:]) != "" {
		return "", false
	}
	var role roleReply
	if json.Unmarshal([]byte(raw[last.start:last.end]), &role) != nil {
		return "", false
	}
	if strings.TrimSpace(role.Reply) == "" {
		spoken := false
		for _, message := range role.Messages {
			if strings.TrimSpace(message) != "" {
				spoken = true
				break
			}
		}
		if !spoken {
			return "", false
		}
	}
	return raw[last.start:last.end], true
}

func roleTranscriptDraft(raw string) bool {
	objects, marker := scanRoleTranscript(raw)
	return marker || len(objects) > 1 || (strings.HasPrefix(strings.TrimSpace(raw), `{"thought"`) && !json.Valid([]byte(strings.TrimSpace(raw))))
}

func scanRoleTranscript(raw string) ([]roleTranscriptObject, bool) {
	var objects []roleTranscriptObject
	marker, fenced := false, false
	for pos := 0; pos < len(raw); {
		end := strings.IndexByte(raw[pos:], '\n')
		if end < 0 {
			end = len(raw)
		} else {
			end += pos
		}
		line := raw[pos:end]
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			fenced = !fenced
		} else if !fenced {
			if strings.HasPrefix(trimmed, "[Calling tool ") || trimmed == "Based on the tool results:" {
				marker = true
			}
			if strings.HasPrefix(trimmed, "{") {
				start := pos + strings.IndexByte(line, '{')
				decoder := json.NewDecoder(strings.NewReader(raw[start:]))
				var fields map[string]json.RawMessage
				if decoder.Decode(&fields) == nil && fields != nil {
					valid := true
					for _, key := range []string{"thought", "action", "reply"} {
						var value string
						field, exists := fields[key]
						if !exists || json.Unmarshal(field, &value) != nil {
							valid = false
							break
						}
					}
					var role roleReply
					objectEnd := start + int(decoder.InputOffset())
					if valid && json.Unmarshal([]byte(raw[start:objectEnd]), &role) == nil {
						objects = append(objects, roleTranscriptObject{start, objectEnd})
						pos = objectEnd
						if pos < len(raw) && raw[pos] == '\n' {
							pos++
						}
						continue
					}
				}
			}
		}
		pos = end + 1
	}
	return objects, marker
}
