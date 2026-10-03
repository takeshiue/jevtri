package config

import (
	"fmt"
	"strings"
	"unicode"
)

func SplitComment(line string) (content, comment string, err error) {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
		return "", line, nil
	}
	var quote rune
	escaped, assignment, valueStarted, closedQuote := false, false, false, false
	for index, character := range line {
		if quote != 0 {
			if character == '\n' || character == '\r' {
				return "", "", fmt.Errorf("quoted value must stay on one line")
			}
			if character == quote && !escaped {
				quote, closedQuote = 0, true
			}
			if character == '\\' {
				escaped = !escaped
			} else {
				escaped = false
			}
			continue
		}
		if character == '#' && (index == 0 || commentSpace(line[index-1])) {
			start := index
			for start > 0 && commentSpace(line[start-1]) {
				start--
			}
			return line[:start], line[start:], nil
		}
		if closedQuote {
			if !unicode.IsSpace(character) {
				return "", "", fmt.Errorf("unexpected characters after quoted value")
			}
			continue
		}
		if !assignment && character == '=' {
			assignment = true
			continue
		}
		if assignment && !valueStarted && !unicode.IsSpace(character) {
			valueStarted = true
			if character == '\'' || character == '"' {
				quote = character
			}
		}
	}
	if quote != 0 {
		return "", "", fmt.Errorf("quoted value is not closed")
	}
	return line, "", nil
}

func StripComment(line string) (string, error) {
	content, _, err := SplitComment(line)
	return content, err
}

func commentSpace(character byte) bool {
	return character == ' ' || character == '\t'
}

func unquoteValue(value string) string {
	if len(value) >= 2 && (value[0] == '\'' || value[0] == '"') && value[len(value)-1] == value[0] {
		return value[1 : len(value)-1]
	}
	return value
}

func FormatValue(value string) (string, error) {
	if strings.ContainsFunc(value, func(character rune) bool { return unicode.IsControl(character) && character != '\t' }) {
		return "", fmt.Errorf("configuration value contains a control character")
	}
	for _, candidate := range []string{value, "\"" + value + "\"", "'" + value + "'"} {
		content, err := StripComment("value = " + candidate)
		if err != nil {
			continue
		}
		_, parsed, ok := strings.Cut(content, "=")
		if ok && unquoteValue(strings.TrimSpace(parsed)) == value {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("configuration value cannot be quoted without changing its contents")
}
