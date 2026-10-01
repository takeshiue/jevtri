package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sample = `# jevtri configuration
[general]
minutes = 10
max_bytes = 30000

[log system]
path = /var/log/messages
time_format = syslog

[log app]
path = /opt/app/app.log
time_format = %d-%m-%Y %H:%M:%S
timezone = UTC
read_compressed = yes
mask = order-\d+
mask = card=\S+

[log new]
path = /var/log/new.log
`

func TestParse(t *testing.T) {
	cfg, err := Parse(strings.NewReader(sample), "jevtri.conf")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Minutes != 10 || cfg.MaxBytes != 30000 || cfg.SentLog != DefaultSentLog || len(cfg.Logs) != 3 {
		t.Fatalf("unexpected config %+v", cfg)
	}
	app := cfg.Logs[1]
	if app.Format == nil || app.Location.String() != "UTC" || !app.ReadCompressed || len(app.Masks) != 2 {
		t.Errorf("unexpected app log %+v", app)
	}
	if cfg.Logs[2].Format != nil || cfg.Logs[2].TimeFormat != "" {
		t.Error("a log without time_format should stay undetected")
	}
}

// CF-01: mistakes are reported with their line number.
func TestErrorsHaveLineNumbers(t *testing.T) {
	cases := []struct{ text, want string }{
		{"[log a]\npth = /x\n", "x.conf:2: unknown key \"pth\""},
		{"[log a]\ntime_format = syslog\n", "x.conf:1: [log a] has no path"},
		{"[general]\nminutes = -1\n", "x.conf:2: minutes must be"},
		{"path = /x\n", "x.conf:1: path is outside a section"},
		{"[log a]\npath = /x\n[log a]\npath = /y\n", "x.conf:3: log \"a\" is already defined at line 1"},
		{"[logs a]\n", "x.conf:1: unknown section"},
		{"[log a]\npath = relative.log\n", "x.conf:2: path must be an absolute path"},
		{"[log a]\npath = /x\ntime_format = nonsense\n", "x.conf:1: [log a]: unknown time format"},
		{"[log a]\npath = /x\ntimezone = Mars/Base\n", "x.conf:1: [log a]: unknown timezone"},
	}
	for _, c := range cases {
		_, err := Parse(strings.NewReader(c.text), "x.conf")
		var configErr *Error
		if !errors.As(err, &configErr) || !strings.HasPrefix(err.Error(), c.want) {
			t.Errorf("%q: got %v, want prefix %q", c.text, err, c.want)
		}
	}
}

// EX-03: the key file must not be readable by others.
func TestLoadAPIKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api-key")
	os.WriteFile(path, []byte("secret-value\n"), 0o600)
	if key, err := LoadAPIKey(path); err != nil || key != "secret-value" {
		t.Fatalf("got %q %v", key, err)
	}
	os.Chmod(path, 0o644)
	if _, err := LoadAPIKey(path); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("0644 accepted: %v", err)
	}
	os.Chmod(path, 0o600)
	os.WriteFile(path, []byte("\n"), 0o600)
	if _, err := LoadAPIKey(path); err == nil {
		t.Error("empty key accepted")
	}
	if _, err := LoadAPIKey(filepath.Join(dir, "missing")); err == nil {
		t.Error("missing file accepted")
	}
	// The key value must never appear in error messages.
	os.WriteFile(path, []byte("two keys\n"), 0o600)
	if _, err := LoadAPIKey(path); err == nil || strings.Contains(err.Error(), "two keys") {
		t.Errorf("unexpected: %v", err)
	}
}

// SEC-006: a configuration another user can write would make root read and
// send any file it names, so it is refused. Same for the API key file.
func TestRefusesFilesOthersCanWrite(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "jevtri.conf")
	content := "[log app]\npath = /tmp/app.log\ntime_format = syslog\n"
	if err := os.WriteFile(conf, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(conf); err != nil {
		t.Fatalf("mode 0644 was refused: %v", err)
	}
	for _, mode := range []os.FileMode{0o666, 0o664, 0o622} {
		if err := os.Chmod(conf, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(conf); err == nil {
			t.Errorf("mode %04o was accepted", mode)
		} else if !strings.Contains(err.Error(), "writable by other users") {
			t.Errorf("mode %04o: %v", mode, err)
		}
	}
	key := filepath.Join(dir, "api-key")
	if err := os.WriteFile(key, []byte("secret-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAPIKey(key); err != nil {
		t.Fatalf("mode 0600 was refused: %v", err)
	}
	// A symbolic link to a key owned by someone else is not a regular file.
	link := filepath.Join(dir, "api-key-link")
	if err := os.Symlink(key, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := LoadAPIKey(link); err == nil {
		t.Error("a symbolic link to the key was accepted")
	}
}
