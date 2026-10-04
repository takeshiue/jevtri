package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/takeshiue/jevtri/internal/config"
)

type forbiddenShowInput struct{}

func (forbiddenShowInput) Read([]byte) (int, error) { panic("--show read stdin") }

func showEnvironment(output, errors io.Writer) environment {
	return environment{
		stdin: forbiddenShowInput{}, stdout: output, stderr: errors,
		getenv:     func(string) string { return "" },
		now:        func() time.Time { panic("--show accessed analysis time") },
		newAsker:   func(string) asker { panic("--show created an API client") },
		isTerminal: func() bool { panic("--show entered first-time setup") },
		hostname:   func() (string, error) { panic("--show collected host data") },
		keyPath:    "/no-such-jevtri-key",
	}
}

func showFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "jevtri.conf")
	content := fmt.Sprintf(`# API_KEY=COMMENT_ONLY_CANARY
[general]
minutes = 7
sent_log = %s

[log web]
path = /no-such-jevtri-log/web-json.log
docker_container = web
time_format = docker-json
timezone = UTC
group = shop
group = billing
mask = MASK_ONLY_CANARY
mask = another-private-rule

[log journal]
journal_unit = nginx.service
`, filepath.Join(dir, "not-created", "sent.log"))
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, content
}

func TestShowEffectiveConfigurationWithoutSideEffects(t *testing.T) {
	path, original := showFixture(t)
	for _, jsonOutput := range []bool{false, true} {
		t.Run(fmt.Sprintf("json=%t", jsonOutput), func(t *testing.T) {
			var output, errors bytes.Buffer
			args := []string{"--show", "-c", path}
			if jsonOutput {
				args = append(args, "--json")
			}
			if code := run(args, showEnvironment(&output, &errors)); code != exitOK {
				t.Fatalf("exit %d, stderr %s", code, errors.String())
			}
			text := output.String()
			for _, forbidden := range []string{"COMMENT_ONLY_CANARY", "MASK_ONLY_CANARY", "another-private-rule"} {
				if strings.Contains(text+errors.String(), forbidden) {
					t.Errorf("secret rule or comment displayed")
				}
			}
			if jsonOutput {
				var shown shownConfiguration
				if err := json.Unmarshal(output.Bytes(), &shown); err != nil {
					t.Fatal(err)
				}
				if shown.ConfigFile != path || shown.Minutes != 7 || shown.MaxBytes != 40000 || len(shown.Logs) != 2 {
					t.Fatalf("wrong effective general settings: %#v", shown)
				}
				web, journal := shown.Logs[0], shown.Logs[1]
				if web.Path != "/no-such-jevtri-log/web-json.log" || web.Timezone != "UTC" || web.TimeFormat != "docker-json" || web.MaskRules != 2 || strings.Join(web.Groups, ",") != "shop,billing" {
					t.Errorf("wrong effective log settings: %#v", web)
				}
				if journal.JournalUnit != "nginx.service" || journal.TimeFormat != "rfc3339" || journal.Groups == nil {
					t.Errorf("wrong journal defaults: %#v", journal)
				}
			} else {
				for _, want := range []string{"Configuration file: " + path, "minutes = 7", "max_bytes = 40000", "[log web]", "group = shop", "group = billing", "Additional mask rules: 2", "journal_unit = nginx.service"} {
					if !strings.Contains(text, want) {
						t.Errorf("missing %q", want)
					}
				}
			}
			bytesAfter, err := os.ReadFile(path)
			if err != nil || string(bytesAfter) != original {
				t.Fatal("configuration changed")
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatal("configuration mode changed")
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(path), "not-created")); !os.IsNotExist(err) {
				t.Fatal("audit directory was created")
			}
		})
	}
}

func TestShowRejectsCommandsAndAnalysisOptions(t *testing.T) {
	for _, extra := range [][]string{
		{"--time", "now"}, {"--minutes", "0"}, {"--issue", ""}, {"--verbose=false"},
		{"--group", "shop"}, {"--all-groups=false"}, {"--dry-run=false"}, {"--config-update"},
		{"init"}, {"report"}, {"unexpected"},
	} {
		t.Run(strings.Join(extra, " "), func(t *testing.T) {
			var output, errors bytes.Buffer
			if code := run(append([]string{"--show", "-c", "/not-a-config"}, extra...), showEnvironment(&output, &errors)); code != exitUsage || output.Len() != 0 {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, output.String(), errors.String())
			}
		})
	}
	for _, command := range []string{"init", "report"} {
		var output, errors bytes.Buffer
		if code := run([]string{command, "--show"}, showEnvironment(&output, &errors)); code != exitUsage || output.Len() != 0 {
			t.Fatal("positional --show entered a command")
		}
	}
}

func TestShowMissingUnsafeAndInvalidConfiguration(t *testing.T) {
	for _, kind := range []string{"missing", "directory", "unsafe", "invalid"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config")
			switch kind {
			case "directory":
				path = dir
			case "unsafe":
				if err := os.WriteFile(path, []byte("[general]\n"), 0o666); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0o666); err != nil {
					t.Fatal(err)
				}
			case "invalid":
				if err := os.WriteFile(path, []byte("[SECRET_SECTION_CANARY]\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var output, errors bytes.Buffer
			if code := run([]string{"--show", "--config", path}, showEnvironment(&output, &errors)); code != exitFailure || output.Len() != 0 {
				t.Fatalf("exit %d, stdout %q", code, output.String())
			}
			if !strings.Contains(errors.String(), path) || strings.Contains(errors.String(), "SECRET_SECTION_CANARY") {
				t.Fatalf("unsafe or unhelpful error: %s", errors.String())
			}
			if kind == "missing" {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("missing configuration was created")
				}
			}
		})
	}
}

func TestShowLanguageAndRelativeConfiguration(t *testing.T) {
	path, _ := showFixture(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, path)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ lang, locale, label string }{
		{"en", "ja_JP.UTF-8", "Configuration file:"}, {"ja", "", "設定ファイル:"},
		{"zh-CN", "", "配置文件:"}, {"", "ja_JP.UTF-8", "設定ファイル:"},
	} {
		var output, errors bytes.Buffer
		env := showEnvironment(&output, &errors)
		env.getenv = func(name string) string {
			if name == "LANG" {
				return c.locale
			}
			return ""
		}
		args := []string{"--show", "-c", relative}
		if c.lang != "" {
			args = append(args, "--lang", c.lang)
		}
		if code := run(args, env); code != exitOK || !strings.Contains(output.String(), c.label+" "+path) {
			t.Fatalf("language/path: exit %d, %s", code, output.String())
		}
	}
	var output, errors bytes.Buffer
	if code := run([]string{"--show", "-c", path, "--lang", "fr"}, showEnvironment(&output, &errors)); code != exitUsage || output.Len() != 0 {
		t.Fatal("unknown language accepted")
	}
}

func TestShowTextEscapesControlsAndJSONKeepsData(t *testing.T) {
	shown := shownConfiguration{ConfigFile: "/config\x1b[2J", SentLog: "/audit\nspoof", Logs: []shownLog{{Name: "app\u009b2J", Path: "/log\x1b[2J", Timezone: "UTC", Groups: []string{"group\nspoof"}}}}
	var output bytes.Buffer
	writeShownConfiguration(&output, shown, "en")
	if strings.ContainsAny(output.String(), "\x1b\u009b") || strings.Contains(output.String(), "\nspoof") {
		t.Fatal("raw controls in text")
	}
	if !strings.Contains(output.String(), `\x1b[2J`) || !strings.Contains(output.String(), `\x0aspoof`) {
		t.Fatal("controls not made visible")
	}
	encoded, err := json.Marshal(shown)
	if err != nil {
		t.Fatal(err)
	}
	var decoded shownConfiguration
	if err := json.Unmarshal(encoded, &decoded); err != nil || decoded.ConfigFile != shown.ConfigFile || decoded.Logs[0].Name != shown.Logs[0].Name {
		t.Fatal("JSON data changed")
	}
}

func TestShowHelpRemainsIndependentOfConfiguration(t *testing.T) {
	var output, errors bytes.Buffer
	if code := run([]string{"--show", "--help", "-c", "/no-config"}, showEnvironment(&output, &errors)); code != exitOK || !strings.Contains(output.String(), config.DefaultPath) {
		t.Fatal("help required a configuration")
	}
}
