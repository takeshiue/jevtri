package setup

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/takeshiue/jevtri/internal/config"
)

func TestUpdateRemoveExistingRegistrations(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		remaining   int
	}{
		{"single", "1\ny\n", 2}, {"range", "1-2\ny\n", 1}, {"all", "all\ny\n", 0},
		{"invalid-retry", "all 1\n2\ny\n", 2}, {"enter", "\n", 3}, {"eof", "", 3},
		{"decline", "1\nn\n", 3}, {"confirm-enter", "1\n\n", 3}, {"confirm-eof", "all\n", 3},
		{"confirm-all", "all\nall\n", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := fakeRoot(t, ubuntu, map[string]string{
				"/var/log/syslog": "ubuntu2404/syslog", "/var/log/auth.log": "ubuntu2404/auth.log", "/var/log/nginx/error.log": "nginx/error.log",
			})
			if err := os.MkdirAll(filepath.Join(root, "var/lib/docker/containers"), 0o755); err != nil {
				t.Fatal(err)
			}
			conf := filepath.Join(t.TempDir(), "jevtri.conf")
			retained := "[log nginx-error]\n# keep this entry\npath = /var/log/nginx/error.log\ntime_format = slash-ymd\ngroup = shop\nmask = keep-secret\n"
			original := configurationSample + "# keep this header\n[general]\nminutes = 17\n\n[log system]\npath = /var/log/syslog\ntime_format = rfc3339\ngroup = system\n\n[log auth]\npath = /var/log/auth.log\ntime_format = rfc3339\ngroup = system\n\n" + retained
			if err := os.WriteFile(conf, []byte(original), 0o640); err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(root, "var/log/syslog")
			before, err := os.ReadFile(source)
			if err != nil {
				t.Fatal(err)
			}
			sourceInfo, err := os.Stat(source)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := Update(strings.NewReader(tc.input), &out, Options{Root: root, ConfigPath: conf}); err != nil {
				t.Fatalf("%v\n%s", err, out.String())
			}
			cfg, err := config.Load(conf)
			if err != nil {
				t.Fatal(err)
			}
			if len(cfg.Logs) != tc.remaining {
				t.Fatalf("saved %d logs, want %d\n%s", len(cfg.Logs), tc.remaining, out.String())
			}
			data, err := os.ReadFile(conf)
			if err != nil {
				t.Fatal(err)
			}
			if tc.remaining == 3 && string(data) != original {
				t.Error("keeping registrations changed configuration bytes")
			}
			if tc.remaining > 0 && !strings.Contains(string(data), retained) {
				t.Error("unselected entry comments or mask changed")
			}
			if cfg.Minutes != 17 {
				t.Error("general settings changed")
			}
			info, err := os.Stat(conf)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o640 {
				t.Error("configuration mode changed")
			}
			after, err := os.ReadFile(source)
			if err != nil {
				t.Fatal(err)
			}
			afterInfo, err := os.Stat(source)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) || afterInfo.Mode() != sourceInfo.Mode() {
				t.Error("log bytes or mode changed")
			}
			if tc.name == "single" {
				out.Reset()
				if err := Update(strings.NewReader("\n"), &out, Options{Root: root, ConfigPath: conf}); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(out.String(), "Found 1 logs that are not in") {
					t.Errorf("removed registration was not offered on next update: %s", out.String())
				}
			}
		})
	}
}

func TestParseSelectionAllEmptyCounts(t *testing.T) {
	for _, count := range []int{0, 1} {
		all, err := ParseSelection("all", count)
		if err != nil {
			t.Fatal(err)
		}
		enter, err := ParseSelection("", count)
		if err != nil {
			t.Fatal(err)
		}
		if len(all) != count || !equal(all, enter) {
			t.Fatalf("count %d: all %v, enter %v", count, all, enter)
		}
	}
}

func TestRemovedRegistrationDiscardsConflictingChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jevtri.conf")
	original := "[log drop]\npath = /missing\ntime_format = rfc3339\n[log keep]\npath = /other\ntime_format = rfc3339\n"
	if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	groups := map[string][]string{"drop": {"obsolete"}, "keep": {"retained"}}
	changes := dockerChanges{Paths: map[string]string{"drop": "invalid\npath"}, Formats: map[string]string{"drop": "invalid\nformat"}, Projects: map[string][]Candidate{"drop": {{Name: "obsolete", Path: "/obsolete"}}}}
	pruneRemovedChanges([]string{"drop"}, groups, changes)
	if len(changes.Paths)+len(changes.Formats)+len(changes.Projects) != 0 || len(groups) != 1 {
		t.Fatal("removed registration kept obsolete changes")
	}
	if err := saveRegisteredUpdate(path, []string{"drop"}, groups, nil, changes, ""); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Logs) != 1 || cfg.Logs[0].Name != "keep" || len(cfg.Logs[0].Groups) != 1 || cfg.Logs[0].Groups[0] != "retained" {
		t.Fatalf("unexpected kept config: %+v", cfg.Logs)
	}
}
