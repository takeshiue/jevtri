//go:build linux

package setup

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/takeshiue/jevtri/internal/config"
)

func TestDeleteLogIdentityAndFileTypes(t *testing.T) {
	requireRealDeletionUID(t)
	for _, kind := range []string{"regular", "symlink", "hardlink", "directory", "fifo", "writable-parent", "leaf-swap", "parent-swap", "parent-mode-change", "ancestor-symlink"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			parent := filepath.Join(root, "logs")
			if err := os.Mkdir(parent, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(parent, "app.log")
			if err := os.WriteFile(path, []byte("keep or delete"), 0o600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "ancestor-symlink":
				alias := filepath.Join(root, "alias")
				if err := os.Symlink(parent, alias); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(alias, "app.log")
			case "symlink":
				os.Remove(path)
				if err := os.Symlink(filepath.Join(root, "target"), path); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(path, filepath.Join(parent, "alias")); err != nil {
					t.Fatal(err)
				}
			case "directory":
				os.Remove(path)
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				os.Remove(path)
				if err := syscall.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
			case "writable-parent":
				if err := os.Chmod(parent, 0o777); err != nil {
					t.Fatal(err)
				}
			}
			target, err := openDeletableLog(path)
			unsafe := kind == "ancestor-symlink" || kind == "symlink" || kind == "hardlink" || kind == "directory" || kind == "fifo" || kind == "writable-parent"
			if unsafe {
				if err == nil {
					target.Close()
					t.Fatal("unsafe file accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer target.Close()
			switch kind {
			case "leaf-swap":
				if err := os.Rename(path, path+".old"); err != nil {
					t.Fatal(err)
				}
				os.WriteFile(path, []byte("replacement"), 0o600)
			case "parent-swap":
				if err := os.Rename(parent, parent+".old"); err != nil {
					t.Fatal(err)
				}
				os.Mkdir(parent, 0o700)
				os.WriteFile(path, []byte("replacement"), 0o600)
			case "parent-mode-change":
				os.Chmod(parent, 0o777)
			}
			err = target.Delete()
			if kind == "regular" {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatal("file remained")
				}
			} else {
				if err == nil {
					t.Fatal("changed file or parent was deleted")
				}
				if _, err := os.Lstat(path); err != nil {
					t.Fatal("replacement not preserved", err)
				}
			}
		})
	}
}

func TestUpdateExplicitFileDeletionRequiresSeparateApproval(t *testing.T) {
	requireRealDeletionUID(t)
	for _, answer := range []string{"y", "n", "", "EOF"} {
		t.Run(answer, func(t *testing.T) {
			root := fakeRoot(t, ubuntu, map[string]string{"/var/log/syslog": "ubuntu2404/syslog"})
			os.MkdirAll(filepath.Join(root, "var/lib/docker/containers"), 0o755)
			conf := filepath.Join(t.TempDir(), "jevtri.conf")
			if err := os.WriteFile(conf, []byte(configurationSample+"[log system]\npath = /var/log/syslog\ntime_format = rfc3339\ngroup = system\n"), 0o640); err != nil {
				t.Fatal(err)
			}
			input := "1\ny\n"
			if answer != "EOF" {
				input += answer + "\n"
			}
			var out bytes.Buffer
			if err := Update(strings.NewReader(input), &out, Options{Root: root, ConfigPath: conf}); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load(conf)
			if err != nil {
				t.Fatal(err)
			}
			if len(cfg.Logs) != 0 {
				t.Fatal("registration remained")
			}
			_, err = os.Stat(filepath.Join(root, "var/log/syslog"))
			if answer == "y" {
				if !os.IsNotExist(err) {
					t.Fatalf("file was not deleted: %v\n%s", err, out.String())
				}
			} else if err != nil {
				t.Fatal("file deleted without separate approval")
			}
		})
	}
}

func TestDeleteRemovedLogProtectedSources(t *testing.T) {
	requireRealDeletionUID(t)
	for _, kind := range []string{"config", "sent-log", "key", "remaining-registration", "remaining-glob", "remaining-alias", "remaining-rotated", "remaining-rotated-gzip", "docker", "unknown-docker"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "app.log")
			os.WriteFile(path, []byte("untouched"), 0o600)
			conf := filepath.Join(root, "jevtri.conf")
			current := "[general]\nsent_log = /other-sent.log\n"
			entry := config.Log{Name: "drop", Path: path}
			opts := Options{ConfigPath: conf}
			switch kind {
			case "config":
				entry.Path = conf
			case "sent-log":
				current = "[general]\nsent_log = " + path + "\n"
			case "key":
				opts.KeyPath = path
			case "remaining-registration":
				current += "[log keep]\npath = " + path + "\ntime_format = rfc3339\ngroup = system\n"
			case "remaining-alias":
				alias := filepath.Join(root, "alias.log")
				if err := os.Symlink(path, alias); err != nil {
					t.Fatal(err)
				}
				current += "[log keep]\npath = " + alias + "\ntime_format = rfc3339\ngroup = system\n"
			case "remaining-rotated", "remaining-rotated-gzip":
				entry.Path = path + ".1"
				if kind == "remaining-rotated-gzip" {
					entry.Path += ".gz"
				}
				os.WriteFile(entry.Path, []byte("keep rotation"), 0o600)
				current += "[log keep]\npath = " + path + "\ntime_format = rfc3339\ngroup = system\n"
			case "remaining-glob":
				current += "[log keep]\npath = " + root + "/*.log\ntime_format = rfc3339\ngroup = system\n"
			case "docker":
				entry.DockerContainer = "live"
			}
			os.WriteFile(conf, []byte(current), 0o600)
			var out bytes.Buffer
			var discoveryError error
			if kind == "unknown-docker" {
				discoveryError = os.ErrPermission
			}
			deleteRemovedHostLogs(bufio.NewReader(strings.NewReader("y\n")), &out, &config.Config{Logs: []config.Log{entry}}, []string{"drop"}, nil, discoveryError, opts)
			if _, err := os.Stat(entry.Path); err != nil {
				t.Fatal("protected file deleted", err)
			}
			if strings.Contains(out.String(), "permanently?") {
				t.Fatalf("protected path offered for deletion: %s", out.String())
			}
		})
	}
}

func requireRealDeletionUID(t *testing.T) {
	t.Helper()
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "Uid:") {
			fields := strings.Fields(line)
			if len(fields) != 5 {
				t.Fatal("kernel UID evidence unavailable")
			}
			uid, err := strconv.Atoi(fields[2])
			if err != nil {
				t.Fatal(err)
			}
			if uid != os.Geteuid() {
				t.Skip("file deletion requires real kernel UID; virtual UID cannot verify directory ownership")
			}
			return
		}
	}
	t.Fatal("kernel UID evidence unavailable")
}

func TestDockerManagedDeletionPaths(t *testing.T) {
	for _, path := range []string{"/var/lib/docker/orphan.log", "/srv/docker/containers/abc/abc-json.log", "/srv/docker/containers/abc/abc-json.log.1.gz"} {
		if !dockerManagedPath(path, nil) {
			t.Errorf("Docker path was not protected: %s", path)
		}
	}
	if dockerManagedPath("/var/log/app.log", nil) {
		t.Fatal("host log classified as Docker managed")
	}
}

func TestDeleteRefusesDeviceMetadata(t *testing.T) {
	requireRealDeletionUID(t)
	if file, err := openDeletableLog("/dev/null"); err == nil {
		file.Close()
		t.Fatal("device accepted")
	}
}

type deletionTestWriter struct {
	buffer  bytes.Buffer
	trigger func(string)
}

func (w *deletionTestWriter) Write(data []byte) (int, error) {
	if w.trigger != nil {
		w.trigger(string(data))
	}
	return w.buffer.Write(data)
}

func TestUpdateSaveFailureNeverOffersFileDeletion(t *testing.T) {
	root := fakeRoot(t, ubuntu, map[string]string{"/var/log/syslog": "ubuntu2404/syslog"})
	os.MkdirAll(filepath.Join(root, "var/lib/docker/containers"), 0o755)
	conf := filepath.Join(t.TempDir(), "jevtri.conf")
	os.WriteFile(conf, []byte(configurationSample+"[log system]\npath = /var/log/syslog\ntime_format = rfc3339\ngroup = system\n"), 0o640)
	source := filepath.Join(root, "var/log/syslog")
	before, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	var out deletionTestWriter
	out.trigger = func(text string) {
		if strings.Contains(text, "Remove these registrations? [y/N]") {
			if err := os.Rename(conf, conf+".saved"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(conf, 0o700); err != nil {
				t.Fatal(err)
			}
			out.trigger = nil
		}
	}
	if err := Update(strings.NewReader("1\ny\ny\n"), &out, Options{Root: root, ConfigPath: conf}); err == nil {
		t.Fatal("save failure was ignored")
	}
	after, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("file changed after failed settings save")
	}
	if strings.Contains(out.buffer.String(), "permanently?") {
		t.Fatal("file deletion was offered before successful save")
	}
}

func TestDeleteLogRejectsDifferentUID(t *testing.T) {
	requireRealDeletionUID(t)
	if os.Geteuid() != 0 {
		t.Skip("different-UID ownership regression requires real root")
	}
	for _, afterApproval := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "app.log")
		if err := os.WriteFile(path, []byte("preserve"), 0o600); err != nil {
			t.Fatal(err)
		}
		var target *deletableLog
		if afterApproval {
			var err error
			target, err = openDeletableLog(path)
			if err != nil {
				t.Fatal(err)
			}
			defer target.Close()
		}
		if err := os.Chown(path, 65534, 65534); err != nil {
			t.Fatal(err)
		}
		if afterApproval {
			if err := target.Delete(); err == nil {
				t.Fatal("changed file owner was accepted")
			}
		} else {
			if file, err := openDeletableLog(path); err == nil {
				file.Close()
				t.Fatal("different file owner was accepted")
			}
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatal("file not preserved", err)
		}
	}
}
