package printsafe

import "testing"

func TestLine(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain text", "plain text"},
		{"keeps\ttab", "keeps\ttab"},
		{"clear\x1b[2Jscreen", "clear\\x1b[2Jscreen"},
		{"bell\x07", "bell\\x07"},
		{"del\x7f", "del\\x7f"},
		{"c1\u0090here", "c1\\x90here"},
		{"日本語はそのまま", "日本語はそのまま"},
		{"bad\xffbyte", "bad\\xffbyte"},
	}
	for _, c := range cases {
		if got := Line(c.in); got != c.want {
			t.Errorf("Line(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
