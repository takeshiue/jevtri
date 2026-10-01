// Package printsafe makes text from a log file or from Jev safe to write to a
// terminal. Escape sequences in such text can move the cursor, clear the
// screen or rewrite what the operator already read (SEC-005).
package printsafe

import (
	"strings"
	"unicode/utf8"
)

// Line replaces every control character, and every byte that is not valid
// UTF-8, with a visible escape. Tabs are kept because they only align text.
func Line(s string) string {
	if !needsEscaping(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			b.WriteString(hex(s[i]))
		case r == '\t':
			b.WriteByte('\t')
		case r < 0x20 || r == 0x7f:
			b.WriteString(hex(byte(r)))
		case r >= 0x80 && r <= 0x9f:
			// C1 controls: ESC-equivalents in some terminals.
			b.WriteString(hex(byte(r)))
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

func needsEscaping(s string) bool {
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if (r == utf8.RuneError && size == 1) || (r != '\t' && (r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f))) {
			return true
		}
		i += size
	}
	return false
}

const digits = "0123456789abcdef"

func hex(b byte) string {
	return string([]byte{'\\', 'x', digits[b>>4], digits[b&0x0f]})
}
