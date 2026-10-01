// Package mask removes secrets from log text before it leaves the host.
//
// The default rules are defined in the specification (section 12.5) and were
// checked against the real log samples for false positives.
package mask

import (
	"fmt"
	"regexp"
	"strings"
)

// rule replaces matches of pattern. keep is the index of the capture group
// that stays visible before the masked part (0 for none); the part after the
// kept group is replaced.
type rule struct {
	kind    string
	pattern *regexp.Regexp
	keep    int
	accept  func(match string) bool
}

var defaultRules = []rule{
	{kind: "private-key", pattern: regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`)},
	{kind: "authorization", pattern: regexp.MustCompile(`(?i)(\b(?:proxy-)?authorization\s*[:=]\s*(?:(?:bearer|basic|token|digest)\s+)?)[^\s,;"']+`), keep: 1},
	{kind: "secret", pattern: regexp.MustCompile(`(?i)(\b[A-Za-z0-9_]*?(?:password|passwd|pass|secret|token|api[_-]?key|access[_-]?key|secret[_-]?key|client[_-]?secret|private[_-]?key|session[_-]?id)\b(?:"?\s*=\s*"?|"?\s*:\s+"?|":"))([^\s,;"'&)]+)`), keep: 1,
		accept: func(value string) bool {
			// "(using password: YES)" in MySQL/MariaDB is evidence, not a secret.
			upper := strings.ToUpper(value)
			return upper != "YES" && upper != "NO" && strings.Trim(value, "*") != ""
		}},
	{kind: "url-credential", pattern: regexp.MustCompile(`(\b[a-z][a-z0-9+.-]*://[^/\s:@]+:)[^/\s@]+(@)`), keep: 1},
	{kind: "query-token", pattern: regexp.MustCompile(`(?i)([?&](?:token|access_token|api_key|apikey|key|sig|signature|password|auth)=)[^&\s"]+`), keep: 1},
	{kind: "known-token", pattern: regexp.MustCompile(`\b(?:AKIA[0-9A-Z]{16}|gh[pousr]_[A-Za-z0-9]{36,}|xox[baprs]-[A-Za-z0-9-]{10,}|eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]+)\b`)},
	{kind: "email", pattern: regexp.MustCompile(`\b[A-Za-z0-9._%+-]+(@[A-Za-z0-9.-]+\.[A-Za-z]{2,})\b`), keep: -1,
		accept: func(address string) bool {
			// systemd unit names such as systemd-coredump@0-1-0.service are not addresses.
			for _, suffix := range []string{".service", ".socket", ".timer", ".mount", ".target", ".slice", ".scope", ".path", ".device"} {
				if strings.HasSuffix(address, suffix) {
					return false
				}
			}
			return true
		}},
	{kind: "cookie", pattern: regexp.MustCompile(`(?i)(\b(?:set-)?cookie\s*[:=]\s*)[^\r\n]+`), keep: 1},
	{kind: "card", pattern: regexp.MustCompile(`\b[3-6](?:[ -]?\d){12,18}\b`), accept: plausibleCard},
}

// Masker applies the default rules and any extra patterns.
type Masker struct {
	rules []rule
}

// Result is masked text with the number of replacements per kind.
type Result struct {
	Text   string
	Counts map[string]int
}

// New returns a Masker with the default rules followed by extra, which are
// regular expressions from the log's mask setting; each whole match is
// replaced.
func New(extra []string) (*Masker, error) {
	rules := append([]rule(nil), defaultRules...)
	for _, expression := range extra {
		compiled, err := regexp.Compile(expression)
		if err != nil {
			return nil, fmt.Errorf("mask %q: %w", expression, err)
		}
		rules = append(rules, rule{kind: "custom", pattern: compiled})
	}
	return &Masker{rules: rules}, nil
}

// Apply masks text.
func (m *Masker) Apply(text string) Result {
	counts := map[string]int{}
	for _, r := range m.rules {
		text = r.apply(text, counts)
	}
	return Result{Text: text, Counts: counts}
}

func (r rule) apply(text string, counts map[string]int) string {
	placeholder := "[MASKED:" + r.kind + "]"
	var out strings.Builder
	last := 0
	for _, loc := range r.pattern.FindAllStringSubmatchIndex(text, -1) {
		whole := text[loc[0]:loc[1]]
		var replacement string
		switch {
		case r.kind == "email":
			// Keep the domain: it tells which destination failed.
			if r.accept != nil && !r.accept(whole) {
				continue
			}
			replacement = placeholder + text[loc[2]:loc[3]]
		case r.keep > 0:
			kept := text[loc[2*r.keep]:loc[2*r.keep+1]]
			masked := text[loc[2*r.keep+1]:loc[1]]
			suffix := ""
			if r.kind == "url-credential" {
				masked = strings.TrimSuffix(masked, "@")
				suffix = "@"
			}
			if r.accept != nil && !r.accept(masked) {
				continue
			}
			replacement = kept + placeholder + suffix
		default:
			if r.accept != nil && !r.accept(whole) {
				continue
			}
			replacement = placeholder
		}
		out.WriteString(text[last:loc[0]])
		out.WriteString(replacement)
		last = loc[1]
		counts[r.kind]++
	}
	if last == 0 {
		return text
	}
	out.WriteString(text[last:])
	return out.String()
}

// plausibleCard applies the Luhn check and rejects runs of one digit, so
// that long timestamps and zero-filled fields are not taken for card numbers.
func plausibleCard(candidate string) bool {
	var digits []int
	distinct := map[rune]bool{}
	for _, c := range candidate {
		if c >= '0' && c <= '9' {
			digits = append(digits, int(c-'0'))
			distinct[c] = true
		}
	}
	if len(digits) < 13 || len(distinct) < 2 {
		return false
	}
	sum := 0
	for i := range digits {
		d := digits[len(digits)-1-i]
		if i%2 == 1 {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
	}
	return sum%10 == 0
}
