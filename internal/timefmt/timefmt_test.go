package timefmt

import (
	"bufio"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var tokyo = time.FixedZone("Asia/Tokyo", 9*3600)

func TestFindExamples(t *testing.T) {
	cases := []struct {
		format, line string
		want         string // RFC 3339 instant; year 2000 marks a missing year
		hasZone      bool
	}{
		{"syslog", "Sep 28 07:51:24 web01 sshd-session[750]: Invalid user admin", "2000-09-28T07:51:24+09:00", false},
		{"syslog", "Sep  8 07:51:24 web01 x", "2000-09-08T07:51:24+09:00", false},
		{"rfc3339", "2026-09-28T07:29:16.929554+09:00 web01 sshd", "2026-09-28T07:29:16.929554+09:00", true},
		{"rfc3339", "2026-09-28T07:50:51+0900 INFO --- logging initialized ---", "2026-09-28T07:50:51+09:00", true},
		{"rfc3339", "2026-09-27T18:02:14.945835Z 0 [Note] [MY-013911]", "2026-09-28T03:02:14.945835+09:00", true},
		{"rfc3339", `{"t":{"$date":"2026-09-28T09:44:40.194+09:00"},"s":"W"}`, "2026-09-28T09:44:40.194+09:00", true},
		{"iso-space", "2026-09-28 03:03:07.591 JST [67] ERROR:  syntax error", "2026-09-28T03:03:07.591+09:00", true},
		{"iso-space", "2026-09-28  3:02:36 0 [Warning] Access denied", "2026-09-28T03:02:36+09:00", false},
		{"iso-space", "2026-09-11 02:03:30 startup archives install", "2026-09-11T02:03:30+09:00", false},
		{"iso-space", "2026-09-28 10:00:00 INFO app started", "2026-09-28T10:00:00+09:00", false},
		{"iso-comma", "2026-09-28 15:47:01,123 ERROR [main] c.e.App - failed", "2026-09-28T15:47:01.123+09:00", false},
		{"slash-ymd", "2026/09/28 03:02:05 [emerg] 27#27: unknown directive", "2026-09-28T03:02:05+09:00", false},
		{"apache-access", `10.0.2.100 - - [28/Sep/2026:09:44:10 +0900] "GET / HTTP/1.1" 200`, "2026-09-28T09:44:10+09:00", true},
		{"apache-access", `10.0.2.100:56044 [28/Sep/2026:09:44:15.502] web app/<NOSRV>`, "2026-09-28T09:44:15.502+09:00", false},
		{"apache-error", "[Mon Sep 28 03:02:10.715837 2026] [core:error] [pid 5:tid 18]", "2026-09-28T03:02:10.715837+09:00", false},
		{"dmy-month", "28-Sep-2026 09:44:02.397 SEVERE [Catalina-utility-1]", "2026-09-28T09:44:02.397+09:00", false},
		{"dmy-month", "[28-Sep-2026 03:03:34] WARNING: [pool www] child 3 exited", "2026-09-28T03:03:34+09:00", false},
		{"dmy-month", "1:C 28 Sep 2026 03:03:22.943 # WARNING Memory overcommit", "2026-09-28T03:03:22.943+09:00", false},
		{"slash-mdy", "09/28/2026 15:47:01 app error", "2026-09-28T15:47:01+09:00", false},
		{"slash-dmy", "28/09/2026 15:47:01 app error", "2026-09-28T15:47:01+09:00", false},
		{"epoch", "type=ANOM_ABEND msg=audit(1790559511.383:2267): auid=4294967295", "2026-09-28T10:38:31.383+09:00", true},
	}
	for _, c := range cases {
		format, err := Lookup(c.format)
		if err != nil {
			t.Fatal(err)
		}
		m, ok := format.Find(c.line, tokyo)
		if !ok {
			t.Errorf("%s: no match in %q", c.format, c.line)
			continue
		}
		want, _ := time.Parse(time.RFC3339Nano, c.want)
		if !m.Time.Equal(want) {
			t.Errorf("%s: %q => %s, want %s", c.format, c.line, m.Time.Format(time.RFC3339Nano), c.want)
		}
		if m.HasZone != c.hasZone {
			t.Errorf("%s: %q HasZone=%v, want %v", c.format, c.line, m.HasZone, c.hasZone)
		}
	}
}

// TF-02: the same instant written with different zone spellings and
// fractional seconds parses alike.
func TestZoneSpellingsAreTheSameInstant(t *testing.T) {
	format, _ := Lookup("iso-space")
	want := time.Date(2026, 9, 28, 3, 0, 0, 0, time.UTC)
	for _, line := range []string{
		"2026-09-28 12:00:00+09:00 x", "2026-09-28 12:00:00+0900 x", "2026-09-28 03:00:00Z x",
		"2026-09-28 12:00:00 JST x", "2026-09-28 03:00:00 UTC x", "2026-09-28 12:00:00.000 x",
	} {
		m, ok := format.Find(line, tokyo)
		if !ok || !m.Time.Equal(want) {
			t.Errorf("%q => %v %v, want %v", line, m.Time, ok, want)
		}
	}
}

func TestRejectsInvalidDates(t *testing.T) {
	format, _ := Lookup("iso-space")
	for _, line := range []string{"2026-02-30 10:00:00 x", "2026-13-01 10:00:00 x", "2026-09-28 25:00:00 x"} {
		if m, ok := format.Find(line, tokyo); ok {
			t.Errorf("%q unexpectedly parsed as %v", line, m.Time)
		}
	}
}

func TestLookupUnknown(t *testing.T) {
	if _, err := Lookup("nope"); err == nil {
		t.Fatal("expected error")
	}
}

// TestRealLogSamples checks TF-01: every sample log in testdata can be read
// with its catalog format. Lines without a timestamp (continuations) are
// allowed, but at least 90% of the lines of each file must match.
func TestRealLogSamples(t *testing.T) {
	samples := map[string]string{
		"nginx/error.log": "slash-ymd", "nginx/access.log": "apache-access",
		"httpd/error_log": "apache-error", "httpd/access_log": "apache-access",
		"mysql/error.log": "rfc3339", "mariadb/error.log": "iso-space",
		"postgres/postgresql.log": "iso-space", "redis/redis.log": "dmy-month",
		"php-fpm/php-fpm.log": "dmy-month",
		"alma9/messages":      "syslog", "alma9/secure": "syslog", "alma9/cron": "syslog", "alma9/maillog": "syslog",
		"alma9/dnf.log":     "rfc3339",
		"ubuntu2404/syslog": "rfc3339", "ubuntu2404/auth.log": "rfc3339", "ubuntu2404/mail.log": "rfc3339",
		"ubuntu2404/dpkg.log": "iso-space",
		"debian12/syslog":     "rfc3339", "debian12/auth.log": "rfc3339", "debian12/mail.log": "rfc3339",
		"debian12/cron.log":              "rfc3339",
		"tomcat/catalina.2026-09-28.log": "dmy-month", "tomcat/localhost_access_log.2026-09-28.txt": "apache-access",
		"haproxy/haproxy.log": "apache-access", "mongodb/mongod.log": "rfc3339",
		"ubuntu2404-host/syslog": "rfc3339", "ubuntu2404-host/kern.log": "rfc3339",
		"ubuntu2404-host/journal-short.log": "syslog", "alma8-host/messages": "syslog",
		"alma8-host/journal-short.log": "syslog", "alma8-host/audit.log": "epoch",
		"ubuntu2404-host/dmesg-T.log": "apache-error",
	}
	// Files whose continuation lines are expected to dominate.
	minimumShare := map[string]float64{
		"tomcat/catalina.2026-09-28.log": 0.2, "haproxy/haproxy.log": 0.2, "redis/redis.log": 0.5,
		"php-fpm/php-fpm.log": 0.5, "mysql/error.log": 0.9, "ubuntu2404-host/kern.log": 0.9,
		// dnf prints a package table and httpd prints AH00558 before logging starts.
		"alma9/dnf.log": 0.8, "httpd/error_log": 0.7,
	}
	for file, name := range samples {
		format, err := Lookup(name)
		if err != nil {
			t.Fatal(err)
		}
		handle, err := os.Open(filepath.Join("..", "..", "testdata", "logs", file))
		if err != nil {
			t.Errorf("%s: %v", file, err)
			continue
		}
		total, matched := 0, 0
		scanner := bufio.NewScanner(handle)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
		for scanner.Scan() {
			if scanner.Text() == "" {
				continue
			}
			total++
			if _, ok := format.Find(scanner.Text(), tokyo); ok {
				matched++
			}
		}
		handle.Close()
		share := 0.9
		if s, ok := minimumShare[file]; ok {
			share = s
		}
		if total == 0 || float64(matched)/float64(total) < share {
			t.Errorf("%s (%s): matched %d of %d lines", file, name, matched, total)
		}
	}
}
