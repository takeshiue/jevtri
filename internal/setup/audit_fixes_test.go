package setup

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/takeshiue/jevtri/internal/config"
)

func TestUpdateGrouplessOnly(t *testing.T) {
	root := fakeRoot(t, ubuntu, map[string]string{"/var/log/syslog": "=2026-10-01T10:00:00Z host ok\n"})
	path := filepath.Join(t.TempDir(), "jevtri.conf")
	before := "# preserve\n[log system]\npath = /var/log/syslog\ntime_format = rfc3339\n"
	if err := os.WriteFile(path, []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Update(strings.NewReader("y\n"), &bytes.Buffer{}, Options{Root: root, ConfigPath: path}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != configurationSample+before+"group = system\n" {
		t.Fatalf("group-only update: %q, %v", data, err)
	}
}

func TestUpdatePreservesIndividualComposeSettings(t *testing.T) {
	for _, setting := range []string{"group = billing", "timezone = Asia/Tokyo", "read_compressed = true", "read_compressed = false", "time_format = docker-json", "# incident-specific comment"} {
		t.Run(setting, func(t *testing.T) {
			root := fakeRoot(t, ubuntu, nil)
			composeContainer(t, root, "a1", "shop-api-1", "json-file", "shop")
			path := filepath.Join(t.TempDir(), "jevtri.conf")
			before := []byte("[log old]\ndocker_container = shop-api-1\n" + setting + "\n")
			if err := os.WriteFile(path, before, 0640); err != nil {
				t.Fatal(err)
			}
			err := Update(strings.NewReader("n\nall\n"), &bytes.Buffer{}, Options{Root: root, ConfigPath: path, Now: time.Now()})
			if err == nil || !strings.Contains(err.Error(), "individual settings or comments") {
				t.Fatalf("unprotected migration: %v", err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("settings changed: %q, %v", after, err)
			}
		})
	}
}

func TestCompleteExistingBlankFormat(t *testing.T) {
	for _, section := range []string{"path = /opt/app/app.log\ntime_format =\n", "time_format =\npath = /opt/app/app.log\n", "path = /opt/app/app.log\ntime_format =\n# keep\ntime_format =\n"} {
		root := fakeRoot(t, ubuntu, map[string]string{"/opt/app/app.log": appLine})
		opts := jevOptions(t, &fakeDetector{answer: "iso-space"}, nil)
		opts.Root = root
		if err := os.WriteFile(opts.ConfigPath, []byte("[log app]\n"+section), 0600); err != nil {
			t.Fatal(err)
		}
		if err := Run(strings.NewReader("y\n"), &bytes.Buffer{}, opts); err != nil {
			t.Fatal(err)
		}
		cfg, err := config.Load(opts.ConfigPath)
		if err != nil || cfg.Logs[0].TimeFormat != "iso-space" {
			t.Fatalf("format was not completed: %v", err)
		}
		data, err := os.ReadFile(opts.ConfigPath)
		if err != nil || strings.Count(string(data), "time_format =") != 1 {
			t.Fatalf("duplicate format: %q", data)
		}
	}
}
func TestDiscoveryConcurrentRotation(t *testing.T) {
	defer func() {
		if failure := recover(); failure != nil {
			t.Fatalf("discovery panicked during file rotation: %v", failure)
		}
	}()
	root := t.TempDir()
	dir := filepath.Join(root, "logs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "app.log")
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = os.WriteFile(path, []byte("2026-10-03T00:00:00Z x\n"), 0600)
			_ = os.Remove(path)
		}
	}()
	defer func() { close(stop); <-done }()
	for i := 0; i < 2000; i++ {
		_ = readableMatches(root, "/logs/*.log")
	}
}
