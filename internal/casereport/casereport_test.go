package casereport

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const answered = `{"time":"%s","dry_run":false,"request":{"state":{"symptom":"s","logs":{"/a":"x","/b":"y"}}},"model":"jev-1","scores":[{"log":"/a","score":0.3},{"log":"/b","score":2.7}]}`

func line(time string) string { return strings.Replace(answered, "%s", time, 1) + "\n" }

// RP-01: only answered runs, from the current and rotated files, newest first.
func TestLoadRunsSkipsWhatCannotBeReported(t *testing.T) {
	dir := t.TempDir()
	sent := filepath.Join(dir, "sent.log")
	os.WriteFile(sent, []byte(line("2026-09-29T10:00:00+09:00")+
		`{"time":"2026-09-29T11:00:00+09:00","dry_run":true,"scores":[{"log":"/a","score":1}]}`+"\n"+
		`{"time":"2026-09-29T12:00:00+09:00","error":"jev could not be reached"}`+"\n"+
		`{"time":"2026-09-29T13:00:00+09:00","purpose":"time_format","options":[{"name":"x","probability":1}]}`+"\n"+
		"not json\n"), 0o600)
	os.WriteFile(sent+".1", []byte(line("2026-09-28T10:00:00+09:00")), 0o600)
	handle, _ := os.Create(sent + ".2.gz")
	gz := gzip.NewWriter(handle)
	gz.Write([]byte(line("2026-09-27T10:00:00+09:00")))
	gz.Close()
	handle.Close()
	os.WriteFile(sent+".bak", []byte(line("2026-09-30T10:00:00+09:00")), 0o600) // not a rotation

	runs, err := LoadRuns(sent)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 {
		t.Fatalf("got %d runs, want 3", len(runs))
	}
	if runs[0].Time.Day() != 29 || runs[2].Time.Day() != 27 {
		t.Errorf("not newest first: %v %v", runs[0].Time, runs[2].Time)
	}
	if runs[0].Rankings[0].Path != "/b" || runs[0].Rankings[0].Priority != 90 {
		t.Errorf("ranking %+v", runs[0].Rankings)
	}
	if _, err := LoadRuns(filepath.Join(dir, "missing.log")); err == nil {
		t.Error("missing send log was not an error")
	}
}

// RP-01: the same value gets the same label; times and names are kept.
func TestPseudonymizer(t *testing.T) {
	p := NewPseudonymizer([]string{"web01.example.net", "web01"})
	got := p.Apply("web01 sshd: Failed from 203.0.113.9 port 22; web01.example.net saw 203.0.113.9 and 2001:db8::1; web010 and a-web01 stay; mysqld 8.0.39")
	want := "[host-2] sshd: Failed from [ip-1] port 22; [host-1] saw [ip-1] and [ip-2]; web010 and a-web01 stay; mysqld 8.0.39"
	if got != want {
		t.Errorf("\ngot  %s\nwant %s", got, want)
	}
	for _, kept := range []string{"03:02:30", "2026-09-28T03:02:30.000+09:00", "aa:bb:cc:dd:ee:ff", "std::string"} {
		if got := p.Apply(kept); got != kept {
			t.Errorf("%s became %s", kept, got)
		}
	}
	for in, want := range map[string]string{
		"192.0.2.1:22":            "[ip-1]:22",
		"[2001:db8::1]:22":        "[[ip-2]]:22",
		"::ffff:192.0.2.1":        "::ffff:[ip-1]", // the prefix is the same for every mapped address
		"2001:DB8::1 2001:db8::1": "[ip-2] [ip-2]",
		"WEB01 up":                "[host-1] up",
		"/var/log/web01/app.log":  "/var/log/[host-1]/app.log",
	} {
		q := NewPseudonymizer([]string{"web01.example.net", "web01"})
		q.Apply("192.0.2.1 2001:db8::1") // fix the label numbers
		if got := q.Apply(in); got != want {
			t.Errorf("%s became %s, want %s", in, got, want)
		}
	}
	for _, address := range []string{"fe80::1", "::1", "2001:db8:0:0:0:0:0:1", "2001:db8::"} {
		if got := p.Apply(address); !strings.HasPrefix(got, "[ip-") {
			t.Errorf("%s was not replaced: %s", address, got)
		}
	}
}

func TestLogTextCannotCloseTheFence(t *testing.T) {
	text := "before\n```\n# injected heading\n"
	if got := fence(text); !strings.HasPrefix(got, "````text\n") || !strings.HasSuffix(got, "\n````") {
		t.Errorf("fence %q", got)
	}
}

// RP-01: paths that name the host are replaced everywhere, including the URL.
func TestHostInPaths(t *testing.T) {
	path := "/var/log/web01/app.log"
	c := Case{Run: Run{Rankings: []Ranking{{Path: path, Priority: 80}}, Logs: map[string]string{path: "x"}}, Causes: []string{path}}
	p := NewPseudonymizer([]string{"web01"})
	if body := Markdown(c, p); strings.Contains(body, "web01") {
		t.Errorf("host name left in the body:\n%s", body)
	}
	if got := FormURL(c, p); strings.Contains(got, "web01") {
		t.Errorf("host name left in the URL: %s", got)
	}
}

// RP-01: text from logs cannot end the code span, split the table row or
// close the folded section, so every masked line stays inside its block.
func TestMarkdownInjection(t *testing.T) {
	path := "/var/log/a`b|c</summary></details><b>x.log"
	c := Case{Run: Run{Rankings: []Ranking{{Path: path, Priority: 80}}, Logs: map[string]string{path: "```\n</details>"}, Symptom: "</details>"}, Causes: []string{path}, Note: "|x|"}
	body := Markdown(c, NewPseudonymizer(nil))
	if strings.Count(body, "</details>") != 1+2 { // one closing tag, and the two inside code fences
		t.Errorf("folded section can be closed early:\n%s", body)
	}
	if strings.Contains(body, "a`b") || strings.Contains(body, "b|c") || strings.Contains(body, "<b>") {
		t.Errorf("path not escaped:\n%s", body)
	}
	if !strings.Contains(body, "| 1 | <code>/var/log/a&#96;b&#124;c&lt;/summary&gt;&lt;/details&gt;&lt;b&gt;x.log</code> | 80 |") {
		t.Errorf("table row:\n%s", body)
	}
}

// RP-01: people's accounts are replaced; system accounts tell which service
// failed and stay.
func TestLocalUsers(t *testing.T) {
	passwd := "root:x:0:0::/root:/bin/bash\nwww-data:x:33:33::/var/www:/usr/sbin/nologin\nalice:x:1000:1000::/home/alice:/bin/bash\nnobody:x:65534:65534::/:/usr/sbin/nologin\nbob:x:1001:1001::/home/bob:/bin/sh\n"
	users := LocalUsers(passwd)
	if strings.Join(users, ",") != "alice,bob" {
		t.Fatalf("users %v", users)
	}
	p := NewPseudonymizer(nil, users...)
	if got := p.Apply("Accepted password for alice; sudo: bob : root; www-data; /home/alice; alicebob"); got != "Accepted password for [user-1]; sudo: [user-2] : root; www-data; /home/[user-1]; alicebob" {
		t.Errorf("got %s", got)
	}
}

func TestFormURL(t *testing.T) {
	got := FormURL(Case{Causes: []string{"/var/log/nginx/error.log"}, OS: "Ubuntu 24.04", JevtriVersion: "0.1.0"}, NewPseudonymizer(nil))
	for _, want := range []string{"template=case.yml", "title=Case%3A+cause+in+%2Fvar%2Flog%2Fnginx%2Ferror.log", "jevtri-version=0.1.0", "os=Ubuntu+24.04"} {
		if !strings.Contains(got, want) {
			t.Errorf("%s lacks %s", got, want)
		}
	}
}
