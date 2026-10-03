package mask

import (
	"regexp"
	"strings"
)

// Each processor owns its parsing strategy; callers need only the masked result.
type processor interface {
	process(text string, counts map[string]int) string
}

type secretProcessor struct{}
type authorizationProcessor struct{}

var secretPrefix = regexp.MustCompile(`(?i)\b[A-Za-z0-9_]*?(?:password|passwd|pass|secret|token|api[_-]?key|access[_-]?key|secret[_-]?key|client[_-]?secret|private[_-]?key|session[_-]?id)\b(?:\\?["']?[ \t]*[=:][ \t]*)`)
var authorizationConfiguration = regexp.MustCompile(`"security"[ \t]*:[ \t]*\{[ \t]*"$`)
var authorizationPrefix = regexp.MustCompile(`(?i)\b(?:proxy-)?authorization\b\\?["']?[ \t]*[:=][ \t]*`)

func (secretProcessor) process(text string, counts map[string]int) string {
	return replaceValues(text, secretPrefix, "secret", false, counts)
}

func (authorizationProcessor) process(text string, counts map[string]int) string {
	return replaceValues(text, authorizationPrefix, "authorization", true, counts)
}

func replaceValues(text string, prefix *regexp.Regexp, kind string, authorization bool, counts map[string]int) string {
	var output strings.Builder
	last := 0
	for _, location := range prefix.FindAllStringIndex(text, -1) {
		if location[0] < last {
			continue
		}
		start := location[1]
		if start >= len(text) {
			continue
		}
		if !authorization && text[start-1] == ':' && text[start] != '"' && text[start] != '\'' && !strings.HasPrefix(text[start:], `\"`) {
			prefixText := text[location[0] : start-1]
			prefixText = strings.TrimRight(prefixText, " \t")
			if !strings.HasSuffix(prefixText, `"`) && !strings.HasSuffix(prefixText, "'") {
				// Package names such as passwd:amd64 are evidence, not assignments.
				continue
			}
		}
		end := start
		if authorization && text[start] != '"' && text[start] != '\'' && !strings.HasPrefix(text[start:], `\"`) {
			wordEnd := start
			for wordEnd < len(text) && text[wordEnd] != ' ' && text[wordEnd] != '\t' && text[wordEnd] != '\r' && text[wordEnd] != '\n' {
				wordEnd++
			}
			scheme := strings.ToLower(text[start:wordEnd])
			if scheme == "bearer" || scheme == "basic" || scheme == "token" || scheme == "digest" {
				start = wordEnd
				for start < len(text) && (text[start] == ' ' || text[start] == '\t') {
					start++
				}
			}
			if scheme == "digest" || (scheme != "bearer" && scheme != "basic" && scheme != "token") {
				// Digest consists of several credentials, so its entire header value is private.
				end = lineEnd(text, start)
			} else {
				start, end = valueSpan(text, start)
			}
		} else {
			start, end = valueSpan(text, start)
		}
		if end <= start {
			continue
		}
		value := text[start:end]
		if authorization && (value == "enabled" || value == "disabled") && authorizationConfiguration.MatchString(text[:location[0]]) {
			// MongoDB's security.authorization setting records whether access checks are enabled.
			continue
		}
		if value == "[MASKED:"+kind+"]" || (!authorization && (strings.EqualFold(value, "YES") || strings.EqualFold(value, "NO") || strings.Trim(value, "*") == "")) {
			continue
		}
		output.WriteString(text[last:start])
		output.WriteString("[MASKED:" + kind + "]")
		last = end
		counts[kind]++
	}
	if last == 0 {
		return text
	}
	output.WriteString(text[last:])
	return output.String()
}

func lineEnd(text string, start int) int {
	if offset := strings.IndexAny(text[start:], "\r\n"); offset >= 0 {
		return start + offset
	}
	return len(text)
}

func valueSpan(text string, start int) (int, int) {
	if start >= len(text) {
		return start, start
	}
	if strings.HasPrefix(text[start:], `\"`) {
		start += 2
		for end := start; end < len(text); end++ {
			if text[end] == '\r' || text[end] == '\n' {
				return start, end
			}
			if text[end] == '\\' {
				slashes := end
				for end < len(text) && text[end] == '\\' {
					end++
				}
				if end < len(text) && text[end] == '"' && end-slashes == 1 {
					return start, slashes
				}
			}
		}
		// A truncated quoted value remains private through the end of the line.
		return start, lineEnd(text, start)
	}
	if text[start] == '"' || text[start] == '\'' {
		quote := text[start]
		start++
		for end := start; end < len(text); end++ {
			if text[end] == '\r' || text[end] == '\n' {
				return start, end
			}
			if text[end] == '\\' {
				end++
				continue
			}
			if text[end] == quote {
				return start, end
			}
		}
		return start, len(text)
	}
	end := start
	for end < len(text) && !strings.ContainsRune(" \t\r\n,;\"'&)}", rune(text[end])) {
		end++
	}
	return start, end
}
