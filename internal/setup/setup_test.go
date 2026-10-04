package setup

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/takeshiue/jevtri/internal/config"
)

var samples = filepath.Join("..", "..", "testdata", "logs")

// fakeRoot builds a host tree under a temporary directory: files maps a
// host path to a sample under testdata/logs, or to literal content when it
// starts with "=".
func fakeRoot(t *testing.T, osRelease string, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	put := func(path string, data []byte) {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put("/etc/os-release", []byte(osRelease))
	for path, source := range files {
		if strings.HasPrefix(source, "=") {
			put(path, []byte(source[1:]))
			continue
		}
		data, err := os.ReadFile(filepath.Join(samples, source))
		if err != nil {
			t.Fatal(err)
		}
		put(path, data)
	}
	return root
}

const ubuntu = "NAME=\"Ubuntu\"\nID=ubuntu\nID_LIKE=debian\n"
const alma = "NAME=\"AlmaLinux\"\nID=\"almalinux\"\nID_LIKE=\"rhel centos fedora\"\n"

func TestDetectFamily(t *testing.T) {
	cases := map[string]Family{
		ubuntu:        FamilyDebian,
		"ID=debian\n": FamilyDebian,
		alma:          FamilyRHEL,
		"ID=rocky\nID_LIKE=\"rhel centos fedora\"\n": FamilyRHEL,
		"ID=alpine\n": FamilyUnknown,
		"":            FamilyUnknown,
	}
	for input, want := range cases {
		if got := DetectFamily(input); got != want {
			t.Errorf("%q: got %v, want %v", input, got, want)
		}
	}
}

func byPath(candidates []Candidate) map[string]Candidate {
	m := map[string]Candidate{}
	for _, c := range candidates {
		m[c.Path] = c
	}
	return m
}

// IN-01 and IN-04: known locations of the Debian family are found with a format that
// reads them; globs are expanded, dated names stay patterns.
func TestFindDebian(t *testing.T) {
	root := fakeRoot(t, ubuntu, map[string]string{
		"/var/log/syslog":                            "ubuntu2404/syslog",
		"/var/log/auth.log":                          "ubuntu2404/auth.log",
		"/var/log/dpkg.log":                          "ubuntu2404/dpkg.log",
		"/var/log/nginx/error.log":                   "nginx/error.log",
		"/var/log/mysql/error.log":                   "mysql/error.log",
		"/var/log/postgresql/postgresql-17-main.log": "postgres/postgresql.log",
		"/var/log/tomcat10/catalina.2026-09-28.log":  "tomcat/catalina.2026-09-28.log",
		"/var/log/sssd/sssd.log":                     "=(2026-09-28  3:02:05): [sssd] [main] started\n",
		"/var/log/sssd/sssd_nss.log":                 "=(2026-09-28  3:02:06): [nss] started\n",
		"/var/log/boot.log":                          "=[  OK  ] Started something.\n",
		"/var/log/fail2ban.log":                      "=2026-09-27 00:00:10,458 fail2ban.server [99]: INFO rollover\n",
	})
	found := Find(FamilyDebian, root)
	got := byPath(found)
	want := map[string]Candidate{
		"/var/log/syslog":                            {Name: "system", TimeFormat: "rfc3339"},
		"/var/log/auth.log":                          {Name: "auth", TimeFormat: "rfc3339"},
		"/var/log/dpkg.log":                          {Name: "package", TimeFormat: "iso-space"},
		"/var/log/nginx/error.log":                   {Name: "nginx-error", TimeFormat: "slash-ymd"},
		"/var/log/mysql/error.log":                   {Name: "mysql", TimeFormat: "rfc3339"},
		"/var/log/postgresql/postgresql-17-main.log": {Name: "postgresql", TimeFormat: "iso-space"},
		"/var/log/tomcat*/catalina.*.log":            {Name: "tomcat", TimeFormat: "dmy-month"},
		"/var/log/sssd/sssd.log":                     {Name: "sssd-sssd", TimeFormat: "iso-space"},
		"/var/log/sssd/sssd_nss.log":                 {Name: "sssd-sssd_nss", TimeFormat: "iso-space"},
		"/var/log/fail2ban.log":                      {Name: "fail2ban", TimeFormat: "iso-comma"},
	}
	if len(found) != len(want) {
		t.Errorf("found %d candidates, want %d: %+v", len(found), len(want), found)
	}
	for path, w := range want {
		c, ok := got[path]
		if !ok {
			t.Errorf("%s not found", path)
			continue
		}
		if c.Name != w.Name || c.TimeFormat != w.TimeFormat {
			t.Errorf("%s: got %s/%s, want %s/%s", path, c.Name, c.TimeFormat, w.Name, w.TimeFormat)
		}
	}
	if found[0].Path != "/var/log/syslog" {
		t.Errorf("the OS logs should come first, got %s", found[0].Path)
	}
}

// IN-04: known locations of the RHEL family; only logs a format can read get
// time_format.
func TestFindRHEL(t *testing.T) {
	root := fakeRoot(t, alma, map[string]string{
		"/var/log/messages":                          "alma9/messages",
		"/var/log/secure":                            "alma9/secure",
		"/var/log/dnf.log":                           "alma9/dnf.log",
		"/var/log/audit/audit.log":                   "alma8-host/audit.log",
		"/var/log/httpd/error_log":                   "httpd/error_log",
		"/var/log/mariadb/mariadb.log":               "mariadb/error.log",
		"/var/lib/pgsql/data/log/postgresql-Mon.log": "postgres/postgresql.log",
		"/var/log/redis/redis.log":                   "redis/redis.log",
		"/var/log/php-fpm/error.log":                 "php-fpm/php-fpm.log",
		"/var/log/haproxy.log":                       "haproxy/haproxy.log",
		"/var/log/mongodb/mongod.log":                "mongodb/mongod.log",
		"/var/log/firewalld":                         "=2025-06-17 11:16:40 WARNING: AllowZoneDrifting is enabled.\n",
		"/var/log/cloud-init.log":                    "=2026-09-26 02:31:02,224 - util.py[DEBUG]: Cloud-init v. 23.4\n",
	})
	got := byPath(Find(FamilyRHEL, root))
	want := map[string]string{
		"/var/log/messages":             "syslog",
		"/var/log/secure":               "syslog",
		"/var/log/dnf.log":              "rfc3339",
		"/var/log/audit/audit.log":      "epoch",
		"/var/log/httpd/error_log":      "apache-error",
		"/var/log/mariadb/mariadb.log":  "iso-space",
		"/var/lib/pgsql/data/log/*.log": "iso-space",
		"/var/log/redis/redis.log":      "dmy-month",
		"/var/log/php-fpm/error.log":    "dmy-month",
		"/var/log/haproxy.log":          "apache-access",
		"/var/log/mongodb/mongod.log":   "rfc3339",
		"/var/log/firewalld":            "iso-space",
		"/var/log/cloud-init.log":       "iso-comma",
	}
	for path, format := range want {
		if c, ok := got[path]; !ok || c.TimeFormat != format {
			t.Errorf("%s: got %+v (found %v), want %s", path, c, ok, format)
		}
	}
	if len(got) != len(want) {
		t.Errorf("found %d, want %d: %+v", len(got), len(want), got)
	}
}

// A file that no known format reads is still offered, without a format.
func TestUnknownFormat(t *testing.T) {
	root := fakeRoot(t, alma, map[string]string{"/var/log/messages": "=no time here\n"})
	found := Find(FamilyRHEL, root)
	if len(found) != 1 || found[0].TimeFormat != "" {
		t.Fatalf("got %+v", found)
	}
}

// IN-04: a just-rotated, empty log is checked against its newest rotated file;
// if that is empty too, the usual format is assumed and marked as such.
func TestEmptyLog(t *testing.T) {
	root := fakeRoot(t, ubuntu, map[string]string{
		"/var/log/syslog":     "=",
		"/var/log/syslog.1":   "=Sep 28 07:51:24 web01 old-style line\n",
		"/var/log/mail.log":   "=",
		"/var/log/mail.log.1": "=",
	})
	got := byPath(Find(FamilyDebian, root))
	if c := got["/var/log/syslog"]; c.TimeFormat != "syslog" || c.Assumed {
		t.Errorf("syslog: %+v", c)
	}
	if c := got["/var/log/mail.log"]; c.TimeFormat != "rfc3339" || !c.Assumed {
		t.Errorf("mail.log: %+v", c)
	}
	if text := Render([]Candidate{got["/var/log/mail.log"]}, time.Now()); !strings.Contains(text, "not a checked one") {
		t.Errorf("assumption not written:\n%s", text)
	}
}

func TestParseSelection(t *testing.T) {
	cases := []struct {
		answer string
		want   []int
		fails  bool
	}{
		{"", []int{0, 1, 2, 3, 4}, false},
		{"all", []int{0, 1, 2, 3, 4}, false},
		{" ALL ", []int{0, 1, 2, 3, 4}, false},
		{"all 1", nil, true},
		{"1,all", nil, true},
		{"all-2", nil, true},
		{"1 3-4", []int{0, 2, 3}, false},
		{"5,1", []int{0, 4}, false},
		{"2-2 2", []int{1}, false},
		{"0", nil, true},
		{"6", nil, true},
		{"4-2", nil, true},
		{"a", nil, true},
	}
	for _, c := range cases {
		got, err := ParseSelection(c.answer, 5)
		if (err != nil) != c.fails || (!c.fails && !equal(got, c.want)) {
			t.Errorf("%q: got %v, %v", c.answer, got, err)
		}
	}
}

func equal(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// IN-01: choosing by number and adding a path writes a configuration that
// config.Load accepts.
func TestRunWritesConfiguration(t *testing.T) {
	root := fakeRoot(t, ubuntu, map[string]string{
		"/var/log/syslog":          "ubuntu2404/syslog",
		"/var/log/auth.log":        "ubuntu2404/auth.log",
		"/var/log/nginx/error.log": "nginx/error.log",
		"/opt/app/app.log":         "=whatever\n",
	})
	confPath := filepath.Join(t.TempDir(), "etc", "jevtri.conf")
	var out bytes.Buffer
	input := "9\n1 3\n/opt/app/app.log\nrelative.log\n/opt/app/missing.log\n\n"
	err := Run(strings.NewReader(input), &out, Options{Root: root, ConfigPath: confPath, Now: time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	for _, text := range []string{"Debian family", "is not a number or range", "give an absolute path", "not a readable file", "Wrote ", "/opt/app/app.log"} {
		if !strings.Contains(out.String(), text) {
			t.Errorf("output lacks %q:\n%s", text, out.String())
		}
	}
	cfg, err := config.Load(confPath)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, log := range cfg.Logs {
		got = append(got, log.Name+" "+log.Path+" "+log.TimeFormat)
	}
	want := []string{"system /var/log/syslog rfc3339", "nginx-error /var/log/nginx/error.log slash-ymd", "app /opt/app/app.log "}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %q", got)
	}
	info, _ := os.Stat(confPath)
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode %v", info.Mode().Perm())
	}
}

// IN-04: an existing configuration file is never overwritten.
func TestRunNeverOverwrites(t *testing.T) {
	root := fakeRoot(t, ubuntu, map[string]string{"/var/log/syslog": "ubuntu2404/syslog"})
	confPath := filepath.Join(t.TempDir(), "jevtri.conf")
	mine := "# mine\n[log system]\npath = /var/log/syslog\ntime_format = rfc3339\n"
	os.WriteFile(confPath, []byte(mine), 0o644)
	var out bytes.Buffer
	if err := Run(strings.NewReader("\n\n"), &out, Options{Root: root, ConfigPath: confPath}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(confPath); string(data) != mine || !strings.Contains(out.String(), "nothing to do") {
		t.Errorf("the existing file was changed or the message is wrong:\n%s", out.String())
	}
	if err := write(confPath, "x"); !errors.Is(err, ErrExists) {
		t.Errorf("write over an existing file: %v", err)
	}
}

func TestRunNothingSelected(t *testing.T) {
	root := fakeRoot(t, ubuntu, nil)
	confPath := filepath.Join(t.TempDir(), "jevtri.conf")
	if err := Run(strings.NewReader("\n"), &bytes.Buffer{}, Options{Root: root, ConfigPath: confPath}); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := os.Stat(confPath); err == nil {
		t.Error("a file was written")
	}
}

// SEC-004, SEC-005: a log file whose name holds a newline or an escape
// sequence is not offered, written to the configuration, or printed.
func TestControlCharactersInFileNames(t *testing.T) {
	root := fakeRoot(t, ubuntu, map[string]string{"/var/log/postgresql/postgresql-main.log": "=2026-09-28 03:02:30 LOG  ok\n"})
	evil := filepath.Join(root, "var", "log", "postgresql", "postgresql-b\n[general]\nminutes = 999999999\n#.log")
	if err := os.WriteFile(evil, []byte("2026-09-28 03:02:31 LOG  ok\n"), 0o644); err != nil {
		t.Skipf("cannot create such a name: %v", err)
	}
	escaped := filepath.Join(root, "var", "log", "postgresql", "postgresql-\x1b[2Jc.log")
	os.WriteFile(escaped, []byte("2026-09-28 03:02:32 LOG  ok\n"), 0o644)
	confPath := filepath.Join(t.TempDir(), "jevtri.conf")
	var out bytes.Buffer
	if err := Run(strings.NewReader("\n\n"), &out, Options{Root: root, ConfigPath: confPath, Now: time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	data, err := os.ReadFile(confPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "999999999") {
		t.Errorf("lines were injected into the configuration:\n%s", data)
	}
	cfg, err := config.Load(confPath)
	if err != nil {
		t.Fatalf("the written configuration does not load: %v", err)
	}
	if cfg.Minutes != 5 {
		t.Errorf("minutes = %d", cfg.Minutes)
	}
	if strings.ContainsAny(out.String(), "\x1b") {
		t.Errorf("an escape sequence was printed:\n%q", out.String())
	}
}

func TestRunAllSelectionSavesEveryCandidate(t *testing.T) {
	for _, answer := range []string{"all", " ALL ", ""} {
		t.Run(answer, func(t *testing.T) {
			root := fakeRoot(t, ubuntu, map[string]string{
				"/var/log/syslog":          "ubuntu2404/syslog",
				"/var/log/auth.log":        "ubuntu2404/auth.log",
				"/var/log/nginx/error.log": "nginx/error.log",
			})
			confPath := filepath.Join(t.TempDir(), "jevtri.conf")
			var out bytes.Buffer
			if err := Run(strings.NewReader(answer+"\n\n"), &out, Options{Root: root, ConfigPath: confPath}); err != nil {
				t.Fatalf("%v\n%s", err, out.String())
			}
			cfg, err := config.Load(confPath)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"/var/log/syslog", "/var/log/auth.log", "/var/log/nginx/error.log"}
			if len(cfg.Logs) != len(want) {
				t.Fatalf("saved %d logs, want %d", len(cfg.Logs), len(want))
			}
			for i, path := range want {
				if cfg.Logs[i].Path != path {
					t.Errorf("log %d path %q, want %q", i, cfg.Logs[i].Path, path)
				}
			}
			if !strings.Contains(out.String(), "all or Enter for all") {
				t.Errorf("selection prompt missing: %s", out.String())
			}
		})
	}
}
