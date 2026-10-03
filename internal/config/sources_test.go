package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// CF-02: docker_container and journal_unit replace path, get their time
// format without one being written, and are checked before use.
func TestDockerAndJournalKeys(t *testing.T) {
	cfg, err := Parse(strings.NewReader("[log web]\ndocker_container = web\n\n[log dockerd]\njournal_unit = docker.service\n\n[log all]\njournal_unit = *\n"), "test.conf")
	if err != nil {
		t.Fatal(err)
	}
	web, dockerd, all := cfg.Logs[0], cfg.Logs[1], cfg.Logs[2]
	if web.Format == nil || web.Format.Name != "docker-json" || web.Label() != "docker:web" {
		t.Errorf("web: %+v", web)
	}
	if dockerd.Format == nil || dockerd.Format.Name != "rfc3339" || dockerd.Label() != "journal:docker.service" || all.JournalUnit != "*" {
		t.Errorf("journal: %+v %+v", dockerd, all)
	}
	bad := map[string]string{
		"[log x]\ntime_format = syslog\n":    "has no path, docker_container, docker_project or journal_unit",
		"[log x]\ndocker_container = -v\n":   "docker_container must be",
		"[log x]\ndocker_container = a/b\n":  "docker_container must be",
		"[log x]\njournal_unit = --root=/\n": "journal_unit must be",
		"[log x]\njournal_unit = a b\n":      "journal_unit must be",
	}
	for text, want := range bad {
		if _, err := Parse(strings.NewReader(text), "test.conf"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", text, err, want)
		}
	}
}

func TestSavedDockerPaths(t *testing.T) {
	tests := []struct {
		name     string
		metadata string
		format   string
		groups   string
		label    string
	}{
		{"container", "docker_container = web\n", "docker-json", "", "docker:web"},
		{"compose-member", "docker_container = web\ndocker_project = shop\n", "docker-json", "", "docker:web"},
		{"project-metadata", "docker_project = shop\n", "docker-json", "", "compose:shop"},
		{"explicit-policies", "docker_container = web\ndocker_project = shop\ntime_format = rfc3339\ngroup = custom\ngroup = second\n", "rfc3339", "custom,second", "docker:web"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			text := "[log web]\npath = /srv/custom docker/containers/web/current-json.log\n" + test.metadata
			cfg, err := Parse(strings.NewReader(text), "saved.conf")
			if err != nil {
				t.Fatal(err)
			}
			log := cfg.Logs[0]
			if log.Path != "/srv/custom docker/containers/web/current-json.log" || log.Format == nil || log.Format.Name != test.format || strings.Join(log.Groups, ",") != test.groups || log.Label() != test.label {
				t.Fatalf("saved path or policies changed: %+v", log)
			}
		})
	}
}

func TestInvalidSavedDockerPaths(t *testing.T) {
	for _, path := range []string{"relative.log", "/srv/*/current.log", "/srv/current?.log", "/srv/[ab].log", "/srv/current\x00.log", "/srv/current\t.log", "/srv/current\x7f.log", "/srv/current\u0085.log"} {
		t.Run(path, func(t *testing.T) {
			for _, metadata := range []string{"docker_container = web", "docker_project = shop", "docker_container = web\ndocker_project = shop"} {
				_, err := Parse(strings.NewReader("[log web]\npath = "+path+"\n"+metadata+"\n"), "bad.conf")
				if err == nil || !strings.Contains(err.Error(), "bad.conf:") || !strings.Contains(err.Error(), "path") {
					t.Fatalf("path %q metadata %q: %v", path, metadata, err)
				}
			}
		})
	}
	for _, sources := range []string{
		"path = /srv/web.log\njournal_unit = docker.service",
		"docker_container = web\njournal_unit = docker.service",
		"docker_project = shop\njournal_unit = docker.service",
		"path = /srv/web.log\ndocker_container = web\ndocker_project = shop\njournal_unit = docker.service",
	} {
		if _, err := Parse(strings.NewReader("[log web]\n"+sources+"\n"), "bad.conf"); err == nil || !strings.Contains(err.Error(), "journal_unit cannot be combined") {
			t.Fatalf("sources %q: %v", sources, err)
		}
	}
	if _, err := Parse(strings.NewReader("[log web]\ndocker_container = web\ndocker_project = shop\n"), "bad.conf"); err == nil {
		t.Fatal("pathless combined Docker sources accepted")
	}
	if _, err := Parse(strings.NewReader("[log files]\npath = /var/log/*.log\n"), "files.conf"); err != nil {
		t.Fatalf("ordinary file glob rejected: %v", err)
	}
}

func TestLoadLegacyDockerForMigration(t *testing.T) {
	for _, test := range []struct {
		name   string
		source string
		group  string
	}{
		{"container", "docker_container = web", ""},
		{"project", "docker_project = shop", "shop"},
		{"project-policies", "docker_project = shop\ngroup = custom", "custom"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "legacy.conf")
			if err := os.WriteFile(path, []byte("[log legacy]\n"+test.source+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			log := cfg.Logs[0]
			if log.Path != "" || log.TimeFormat != "docker-json" || log.Format == nil || strings.Join(log.Groups, ",") != test.group {
				t.Fatalf("legacy migration policies changed: %+v", log)
			}
		})
	}
}

// CF-03: group may repeat, so a log can be in several groups; bad or doubled
// names are refused with the line.
func TestGroups(t *testing.T) {
	cfg, err := Parse(strings.NewReader("[log pg]\ndocker_container = pg\ngroup = live1\ngroup = onyx\n\n[log sys]\npath = /var/log/syslog\ntime_format = rfc3339\ngroup = system\n"), "test.conf")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cfg.Logs[0].Groups, ","); got != "live1,onyx" || strings.Join(cfg.Logs[1].Groups, ",") != "system" {
		t.Errorf("groups: %q %v", got, cfg.Logs[1].Groups)
	}
	for text, want := range map[string]string{
		"[log x]\npath = /x\ngroup = a b\n":          "group must be",
		"[log x]\npath = /x\ngroup = \n":             "group must be",
		"[log x]\npath = /x\ngroup = a\ngroup = a\n": "given twice",
	} {
		if _, err := Parse(strings.NewReader(text), "test.conf"); err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "test.conf:") {
			t.Errorf("%q: %v", text, err)
		}
	}
}
