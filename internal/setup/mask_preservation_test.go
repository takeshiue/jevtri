package setup

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/takeshiue/jevtri/internal/config"
	"github.com/takeshiue/jevtri/internal/mask"
)

func TestUpdatePreservesMasksBeforeComposeMigration(t *testing.T) {
	cases := []struct {
		name     string
		sections string
	}{
		{"single", "[log protected]\ndocker_container = shop-api-1\nmask = CUSTOMER-[0-9]+\ngroup = payments\n"},
		{"multiple", "[log protected]\ndocker_container = shop-api-1\nmask = CUSTOMER-[0-9]+\nmask = ORDER-[0-9]+\ngroup = payments\n"},
		{"duplicate registration", "[log plain]\ndocker_container = shop-api-1\n\n[log protected]\ndocker_container = shop-api-1\nmask = CUSTOMER-[0-9]+\ngroup = payments\n"},
		{"later member", "[log plain]\ndocker_container = shop-api-1\n\n[log protected]\ndocker_container = shop-db-1\nmask = CUSTOMER-[0-9]+\ngroup = payments\n"},
		{"later project", "[log plain]\ndocker_container = blog-api-1\n\n[log protected]\ndocker_container = shop-api-1\nmask = CUSTOMER-[0-9]+\ngroup = payments\n"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			root := fakeRoot(t, ubuntu, map[string]string{"/var/log/nginx/error.log": "=opaque CUSTOMER-12345\n"})
			composeContainer(t, root, "a1", "shop-api-1", "json-file", "shop")
			composeContainer(t, root, "b2", "shop-db-1", "json-file", "shop")
			composeContainer(t, root, "c3", "blog-api-1", "json-file", "blog")
			detector := &fakeDetector{answer: "iso-space"}
			opts := jevOptions(t, detector, nil)
			opts.Root = root
			before := []byte("# Keep the existing protection and comments.\n[general]\n\n" + test.sections)
			if err := os.WriteFile(opts.ConfigPath, before, 0o640); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err := Update(strings.NewReader(strings.Repeat("n\n", strings.Count(test.sections, "docker_container ="))+"all\nall\ny\n"), &out, opts)
			if err == nil || !strings.Contains(err.Error(), "custom mask rules") {
				t.Fatalf("expected safe migration rejection, got %v", err)
			}
			after, err := os.ReadFile(opts.ConfigPath)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("configuration changed: error=%v", err)
			}
			info, err := os.Stat(opts.ConfigPath)
			if err != nil || info.Mode().Perm() != 0o640 {
				t.Fatalf("configuration permissions changed: %v", err)
			}
			if len(detector.requests) != 0 || strings.Contains(out.String(), "is removed") || strings.Contains(out.String(), "Added ") {
				t.Fatalf("side effects preceded rejection: %s", out.String())
			}
			if strings.Contains(out.String(), "CUSTOMER-[0-9]+") {
				t.Fatal("mask expression disclosed in output")
			}
			cfg, err := config.Load(opts.ConfigPath)
			if err != nil {
				t.Fatal(err)
			}
			for _, log := range cfg.Logs {
				if log.Name != "protected" {
					continue
				}
				masker, err := mask.New(log.Masks)
				if err != nil || strings.Contains(masker.Apply("CUSTOMER-12345 ORDER-123").Text, "CUSTOMER-12345") {
					t.Fatal("existing custom rule no longer protects the source")
				}
			}
		})
	}
}

func TestUpdateDeclinedProtectedComposeMigration(t *testing.T) {
	root := fakeRoot(t, ubuntu, nil)
	composeContainer(t, root, "a1", "shop-api-1", "json-file", "shop")
	conf := filepath.Join(t.TempDir(), "jevtri.conf")
	before := []byte("[log old]\ndocker_container = shop-api-1\nmask = CUSTOMER-[0-9]+\ngroup = payments\n")
	if err := os.WriteFile(conf, before, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Update(strings.NewReader("n\n"), &bytes.Buffer{}, Options{Root: root, ConfigPath: conf}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(conf)
	if err != nil || !bytes.Equal(append([]byte(configurationSample), before...), after) {
		t.Fatal("declining migration changed the configuration")
	}
}

func TestUpdateUnrelatedMaskDoesNotBlockMigration(t *testing.T) {
	root := fakeRoot(t, ubuntu, map[string]string{"/var/log/syslog": "=2026-10-01T10:00:00Z host ok\n"})
	composeContainer(t, root, "a1", "shop-api-1", "json-file", "shop")
	conf := filepath.Join(t.TempDir(), "jevtri.conf")
	before := "[log host]\npath = /var/log/syslog\ntime_format = rfc3339\nmask = CUSTOMER-[0-9]+\ngroup = system\n\n[log old]\ndocker_container = shop-api-1\n"
	if err := os.WriteFile(conf, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Update(strings.NewReader("n\nall\n"), &bytes.Buffer{}, Options{Root: root, ConfigPath: conf, Now: time.Now()}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(conf)
	if err != nil || !strings.Contains(string(after), "mask = CUSTOMER-[0-9]+") || !strings.Contains(string(after), "docker_project = shop") || strings.Contains(string(after), "[log old]") {
		t.Fatalf("unrelated mask or ordinary migration changed: %v", err)
	}
}

func TestUpdateExplicitRemovalOfMissingMaskedLog(t *testing.T) {
	root := fakeRoot(t, ubuntu, map[string]string{"/var/log/syslog": "=2026-10-01T10:00:00Z host ok\n"})
	if err := os.MkdirAll(filepath.Join(root, "/var/lib/docker/containers"), 0700); err != nil {
		t.Fatal(err)
	}
	conf := filepath.Join(t.TempDir(), "jevtri.conf")
	if err := os.WriteFile(conf, []byte("[log gone]\ndocker_container = gone\nmask = CUSTOMER-[0-9]+\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Update(strings.NewReader("\nall\nall\n"), &bytes.Buffer{}, Options{Root: root, ConfigPath: conf}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(conf)
	if err != nil || strings.Contains(string(after), "[log gone]") || !strings.Contains(string(after), "path = /var/log/syslog") {
		t.Fatalf("explicit removal did not complete: %v", err)
	}
}
