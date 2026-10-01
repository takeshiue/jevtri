package timefmt

import (
	"testing"
	"time"
)

func TestCustomLayouts(t *testing.T) {
	cases := []struct{ layout, line, want string }{
		{"%d-%m-%Y %H:%M:%S", "app: 28-09-2026 15:47:01 ERROR db down", "2026-09-28T15:47:01+09:00"},
		{"%Y%m%d %H:%M:%S.%f", "20260928 15:47:01.25 x", "2026-09-28T15:47:01.25+09:00"},
		{"%a %b %d %H:%M:%S %Z %Y", "Mon Sep 28 06:47:01 UTC 2026 started", "2026-09-28T15:47:01+09:00"},
		{"ts=%s", "level=error ts=1790559511 msg=x", "2026-09-28T10:38:31+09:00"},
		{"[%Y/%m/%d %H:%M:%S %z]", "[2026/09/28 15:47:01 +0000] x", "2026-09-29T00:47:01+09:00"},
	}
	for _, c := range cases {
		format, err := Resolve(c.layout)
		if err != nil {
			t.Fatalf("%s: %v", c.layout, err)
		}
		m, ok := format.Find(c.line, tokyo)
		if !ok {
			t.Errorf("%s: no match in %q", c.layout, c.line)
			continue
		}
		want, _ := time.Parse(time.RFC3339Nano, c.want)
		if !m.Time.Equal(want) {
			t.Errorf("%s: %q => %s, want %s", c.layout, c.line, m.Time.Format(time.RFC3339Nano), c.want)
		}
	}
}

func TestCustomLayoutErrors(t *testing.T) {
	for _, layout := range []string{"%Y-%m-%d", "%Q %H:%M", "%H:%M %d %H", "abc%", "plain"} {
		if _, err := Resolve(layout); err == nil {
			t.Errorf("%q: expected error", layout)
		}
	}
}

// TF-03: a syslog line without a year gets the year closest to the reference.
func TestResolveYear(t *testing.T) {
	format, _ := Lookup("syslog")
	cases := []struct{ line, reference, want string }{
		{"Sep 28 07:51:24 x", "2026-09-28T08:00:00+09:00", "2026-09-28T07:51:24+09:00"},
		{"Dec 31 23:58:00 x", "2027-01-01T00:02:00+09:00", "2026-12-31T23:58:00+09:00"},
		{"Jan  1 00:01:00 x", "2026-12-31T23:59:00+09:00", "2027-01-01T00:01:00+09:00"},
	}
	for _, c := range cases {
		m, ok := format.Find(c.line, tokyo)
		if !ok {
			t.Fatalf("no match: %q", c.line)
		}
		reference, _ := time.Parse(time.RFC3339, c.reference)
		want, _ := time.Parse(time.RFC3339, c.want)
		if got := ResolveYear(m, reference); !got.Equal(want) {
			t.Errorf("%q ref %s => %s, want %s", c.line, c.reference, got, c.want)
		}
	}
}

// TestRewriteToReferenceZone covers TF-04: a MySQL UTC line is rewritten into
// the reference zone.
func TestRewriteToReferenceZone(t *testing.T) {
	format, _ := Lookup("rfc3339")
	line := "2026-09-27T18:02:21.971050Z 9 [Note] [MY-010926] [Server] Access denied for user 'root'@'localhost'"
	m, _ := format.Find(line, tokyo)
	got := Rewrite(line, m, m.Time, tokyo)
	want := "2026-09-28T03:02:21.971+09:00 9 [Note] [MY-010926] [Server] Access denied for user 'root'@'localhost'"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}
