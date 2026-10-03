package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoredDockerPathDoesNotFollowRecreation(t *testing.T) {
	directory := t.TempDir()
	oldPath := filepath.Join(directory, "old-json.log")
	newPath := filepath.Join(directory, "new-json.log")
	for path, marker := range map[string]string{oldPath: "REGISTERED_OLD", newPath: "UNREGISTERED_NEW"} {
		line := fmt.Sprintf("{\"log\":\"ERROR %s\\n\",\"stream\":\"stderr\",\"time\":\"2026-09-27T18:02:30Z\"}\n", marker)
		if err := os.WriteFile(path, []byte(line), 0600); err != nil {
			t.Fatal(err)
		}
	}
	f := newFixture(t, "")
	configuration := fmt.Sprintf("[general]\nsent_log = %s\n[log registered]\npath = %s\ndocker_container = api\ndocker_project = shop\ngroup = shop\n", filepath.Join(directory, "sent.log"), oldPath)
	if err := os.WriteFile(f.conf, []byte(configuration), 0600); err != nil {
		t.Fatal(err)
	}
	jst := time.FixedZone("JST", 9*3600)
	env := environment{stdout: &f.stdout, stderr: &f.stderr, now: func() time.Time { return time.Date(2026, 9, 28, 4, 0, 0, 0, jst) }, newAsker: func(string) asker { return f.asker }, keyPath: f.key, isTerminal: func() bool { return false }}
	if code := run([]string{"-c", f.conf, "-t", "03:02", "--dry-run"}, env); code != exitOK {
		t.Fatalf("code=%d stderr=%s", code, f.stderr.String())
	}
	if !strings.Contains(f.stdout.String(), "REGISTERED_OLD") || strings.Contains(f.stdout.String(), "UNREGISTERED_NEW") {
		t.Fatalf("stored path not respected: %s", f.stdout.String())
	}
	if err := os.Remove(oldPath); err != nil {
		t.Fatal(err)
	}
	f.stdout.Reset()
	f.stderr.Reset()
	if code := run([]string{"-c", f.conf, "-t", "03:02", "--dry-run"}, env); code != exitFailure || f.stdout.Len() != 0 {
		t.Fatalf("missing stored path followed another source: code=%d stdout=%s stderr=%s", code, f.stdout.String(), f.stderr.String())
	}
	if !strings.Contains(f.stderr.String(), "Log file not found:") || !strings.Contains(f.stderr.String(), "--config-update") {
		t.Fatalf("missing actionable disappearance warning: %s", f.stderr.String())
	}
}

func TestLegacyDockerStopsBeforeJournalCollection(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "journal-invoked")
	command := filepath.Join(root, "usr/bin/journalctl")
	if err := os.MkdirAll(filepath.Dir(command), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(command, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"docker_container = web", "docker_project = shop"} {
		t.Run(source, func(t *testing.T) {
			f := newFixture(t, "\n[log journal]\njournal_unit = *\n\n[log legacy]\n"+source+"\n")
			env := environment{stdout: &f.stdout, stderr: &f.stderr, now: time.Now, newAsker: func(string) asker { t.Error("API was prepared"); return f.asker }, keyPath: f.key, isTerminal: func() bool { return false }, root: root}
			if code := run([]string{"-c", f.conf, "--dry-run"}, env); code != exitFailure || !strings.Contains(f.stderr.String(), "--config-update") || f.stdout.Len() != 0 {
				t.Fatalf("code=%d stdout=%s stderr=%s", code, f.stdout.String(), f.stderr.String())
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("journal was invoked before legacy validation: %v", err)
			}
		})
	}
}

func TestGrouplessLogsWarnBeforeSelection(t *testing.T) {
	f := newFixture(t, "")
	if code := f.run("--dry-run"); code != exitOK {
		t.Fatalf("code=%d stderr=%s", code, f.stderr.String())
	}
	if !bytes.Contains(f.stderr.Bytes(), []byte("have no group and are included in every selection")) {
		t.Fatalf("missing group warning: %s", f.stderr.String())
	}
}

func TestMissingRegisteredFilesWarnForEverySource(t *testing.T) {
	directory := t.TempDir()
	for _, test := range []struct {
		name     string
		path     string
		metadata string
		missing  bool
	}{
		{"host", filepath.Join(directory, "mysql.log"), "", true},
		{"pattern", filepath.Join(directory, "mysql-*.log"), "", true},
		{"container", filepath.Join(directory, "container.log"), "docker_container = mysql\n", true},
		{"directory-is-not-missing", directory, "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t, fmt.Sprintf("\n[log removed]\npath = %s\n%s\ntime_format = iso-space\ngroup = database\n", test.path, test.metadata))
			if code := f.run("--dry-run", "-t", "03:02"); code != exitPartial {
				t.Fatalf("code=%d stderr=%s", code, f.stderr.String())
			}
			warning := "Warning: Log file not found: " + test.path
			if strings.Contains(f.stderr.String(), warning) != test.missing {
				t.Fatalf("missing=%v stderr=%s", test.missing, f.stderr.String())
			}
			if test.missing && !strings.Contains(f.stderr.String(), "--config-update") {
				t.Fatalf("configuration update not advised: %s", f.stderr.String())
			}
		})
	}
}

func TestMissingLogWarningUsesLocale(t *testing.T) {
	for _, test := range []struct {
		locale string
		first  string
		last   string
	}{
		{"C", "Warning: Log file not found:", "check and update your config."},
		{"en_US.UTF-8", "Warning: Log file not found:", "check and update your config."},
		{"ja_JP.UTF-8", "警告: ログファイルが見つかりません:", "設定を確認更新するには 'jevtri --config-update' を実行してください。"},
		{"zh_CN.UTF-8", "警告: 找不到日志文件:", "请运行 'jevtri --config-update' 检查并更新配置。"},
	} {
		t.Run(test.locale, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "missing.log")
			f := newFixture(t, fmt.Sprintf("\n[log missing]\npath = %s\ntime_format = iso-space\ngroup = database\n", path))
			jst := time.FixedZone("JST", 9*3600)
			env := environment{stdout: &f.stdout, stderr: &f.stderr, now: func() time.Time { return time.Date(2026, 9, 28, 4, 0, 0, 0, jst) }, newAsker: func(string) asker { return f.asker }, keyPath: f.key, isTerminal: func() bool { return false }, getenv: func(name string) string {
				if name == "LANG" {
					return test.locale
				}
				return ""
			}}
			if code := run([]string{"-c", f.conf, "--dry-run", "-t", "03:02"}, env); code != exitPartial {
				t.Fatalf("code=%d stderr=%s", code, f.stderr.String())
			}
			if !strings.Contains(f.stderr.String(), test.first+" "+path) || !strings.Contains(f.stderr.String(), test.last) || strings.Contains(f.stderr.String(), "log is not readable:") {
				t.Fatalf("wrong localized warning: %s", f.stderr.String())
			}
		})
	}
}
