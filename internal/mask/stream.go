package mask

import (
	"encoding/json"
	"regexp"
	"strings"
)

var privateBegin = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)
var privateEnd = regexp.MustCompile(`-----END [A-Z ]*PRIVATE KEY-----`)
var pemBody = regexp.MustCompile(`^[A-Za-z0-9+/]+={0,2}$`)

// Stream retains block state before time-window and retention filtering.
// Only fixed stream slots are used, so untrusted labels cannot grow memory.
type Stream struct{ active [3]bool }

func NewStream() *Stream { return &Stream{} }

// Apply preserves a Docker envelope while masking its decoded log payload.
func (s *Stream) Apply(text string) Result {
	var fields map[string]json.RawMessage
	if strings.HasPrefix(strings.TrimSpace(text), "{") && json.Unmarshal([]byte(text), &fields) == nil {
		var payload string
		if raw, ok := fields["log"]; ok && json.Unmarshal(raw, &payload) == nil {
			var name string
			_ = json.Unmarshal(fields["stream"], &name)
			slot := 0
			if name == "stdout" {
				slot = 1
			} else if name == "stderr" {
				slot = 2
			}
			masked := s.applyPayload(payload, slot, true)
			if masked.Text == payload {
				return Result{Text: text, Counts: masked.Counts}
			}
			fields["log"], _ = json.Marshal(masked.Text)
			encoded, _ := json.Marshal(fields)
			masked.Text = string(encoded)
			return masked
		}
	}
	return s.applyPayload(text, 0, true)
}

func (s *Stream) applyPayload(text string, slot int, orphan bool) Result {
	const hidden = "[MASKED:private-key]"
	counts := map[string]int{}
	var out strings.Builder
	cursor := 0
	for cursor < len(text) {
		if s.active[slot] {
			end := privateEnd.FindStringIndex(text[cursor:])
			out.WriteString(hidden)
			counts["private-key"]++
			if end == nil {
				break
			}
			cursor += end[1]
			s.active[slot] = false
			continue
		}
		begin := privateBegin.FindStringIndex(text[cursor:])
		if begin == nil {
			out.WriteString(maskOrphan(text[cursor:], counts, orphan))
			break
		}
		out.WriteString(maskOrphan(text[cursor:cursor+begin[0]], counts, orphan))
		cursor += begin[1]
		s.active[slot] = true
		// The marker may be the whole event; its payload still must be hidden.
		if cursor == len(text) {
			out.WriteString(hidden)
			counts["private-key"]++
		}
	}
	return Result{Text: out.String(), Counts: counts}
}

func maskOrphan(text string, counts map[string]int, enabled bool) string {
	if !enabled {
		return text
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		body := strings.TrimSpace(line)
		prefix := ""
		if i := strings.LastIndexAny(body, " \t"); i >= 0 {
			prefix, body = body[:i+1], body[i+1:]
		}
		// A seek or rotation can omit BEGIN. Prefer confidentiality for a whole
		// PEM-width base64 payload; this intentionally hides some non-secret base64.
		if len(body) >= 16 && len(body) <= 76 && pemBody.MatchString(body) {
			lines[i] = prefix + "[MASKED:private-key]"
			counts["private-key"]++
		}
	}
	return strings.Join(lines, "\n")
}
