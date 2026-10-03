package docker

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// writeContainer writes Docker's metadata for one container under root, in the
// shape Docker 20+ writes it (only the fields jevtri reads, plus a few others).
func writeContainer(t *testing.T, root, id, name, driver, logPath string) {
	t.Helper()
	dir := filepath.Join(root, ContainersDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	config := `{"StreamConfig":{},"State":{"Running":true,"Pid":1234},"ID":"` + id + `","Name":"/` + name + `","LogPath":"` + logPath + `","Driver":"overlay2"}`
	host := `{"LogConfig":{"Type":"` + driver + `","Config":{}},"NetworkMode":"bridge"}`
	os.WriteFile(filepath.Join(dir, "config.v2.json"), []byte(config), 0o600)
	os.WriteFile(filepath.Join(dir, "hostconfig.json"), []byte(host), 0o600)
}

// LR-06: a container is found by name, whatever its ID, and only its own
// json-file log is used.
func TestLogFileByName(t *testing.T) {
	root := t.TempDir()
	web := filepath.Join(ContainersDir, "aaa111", "aaa111-json.log")
	writeContainer(t, root, "aaa111", "web", "json-file", web)
	writeContainer(t, root, "bbb222", "db", "journald", "")
	// Metadata that points outside the container's directory is not followed.
	writeContainer(t, root, "ccc333", "evil", "json-file", "/etc/shadow")

	got, err := LogFile(root, "web")
	if err != nil || got != filepath.Join(root, web) {
		t.Fatalf("web: %q %v", got, err)
	}
	if _, err := LogFile(root, "db"); !errors.Is(err, ErrNoFile) {
		t.Errorf("journald driver: %v", err)
	}
	if _, err := LogFile(root, "evil"); !errors.Is(err, ErrNoFile) {
		t.Errorf("outside path was accepted: %v", err)
	}
	if _, err := LogFile(root, "gone"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing container: %v", err)
	}

	// Recreated: same name, new ID.
	os.RemoveAll(filepath.Join(root, ContainersDir, "aaa111"))
	writeContainer(t, root, "ddd444", "web", "json-file", filepath.Join(ContainersDir, "ddd444", "ddd444-json.log"))
	if got, _ := LogFile(root, "web"); got != filepath.Join(root, ContainersDir, "ddd444", "ddd444-json.log") {
		t.Errorf("recreated container not followed: %q", got)
	}
}

func TestListWithoutDocker(t *testing.T) {
	if _, err := List(t.TempDir()); err == nil {
		t.Error("expected an error without /var/lib/docker/containers")
	}
}
