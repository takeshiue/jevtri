//go:build roottest

package safeopen

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// These run as root in the distribution containers (tests/distro/root.sh),
// where directory ownership is real rather than faked by the test.

func requireRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Fatal("roottest needs root")
	}
}

// RT-01: a service-owned log directory swaps a subdirectory for a link to
// /etc; root must not read /etc/shadow through it.
func TestRootRefusesServiceOwnedLink(t *testing.T) {
	requireRoot(t)
	base := filepath.Join("/var/log", "jevtri-rt01")
	os.RemoveAll(base)
	defer os.RemoveAll(base)
	if err := os.MkdirAll(filepath.Join(base, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(base, "sub", "shadow"), "EXPECTED")
	// nobody (65534) owns the directory, as postgres owns /var/log/postgresql.
	if err := os.Chown(base, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(base, "sub", "shadow")
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	os.Rename(filepath.Join(base, "sub"), filepath.Join(base, "sub.saved"))
	if err := os.Symlink("/etc", filepath.Join(base, "sub")); err != nil {
		t.Fatal(err)
	}
	got, err := readAll(t, path)
	if !errors.Is(err, ErrSymlinkParent) {
		t.Fatalf("read %d bytes, err = %v, want ErrSymlinkParent", len(got), err)
	}
}

// RT-02: a group-writable root-owned directory (root:postgres 1775) is not
// trusted either.
func TestRootRefusesGroupWritableDirectory(t *testing.T) {
	requireRoot(t)
	base := filepath.Join("/var/log", "jevtri-rt02")
	os.RemoveAll(base)
	defer os.RemoveAll(base)
	os.Mkdir(base, 0o755)
	os.Chmod(base, 0o1775)
	os.Symlink("/etc", filepath.Join(base, "sub"))
	if _, err := readAll(t, filepath.Join(base, "sub", "hostname")); !errors.Is(err, ErrSymlinkParent) {
		t.Fatalf("err = %v, want ErrSymlinkParent", err)
	}
}

// RT-03: a link root placed in a root-only directory is followed, so logs
// reached through /var/run or similar keep working.
func TestRootFollowsRootOwnedLink(t *testing.T) {
	requireRoot(t)
	var stat syscall.Stat_t
	if err := syscall.Stat("/var/log", &stat); err != nil || stat.Uid != 0 || stat.Mode&0o022 != 0 {
		t.Fatalf("/var/log is not root-only: uid %d mode %o", stat.Uid, stat.Mode)
	}
	real := filepath.Join("/var/log", "jevtri-rt03-real")
	link := filepath.Join("/var/log", "jevtri-rt03")
	os.RemoveAll(real)
	os.Remove(link)
	defer os.RemoveAll(real)
	defer os.Remove(link)
	os.Mkdir(real, 0o755)
	mustWrite(t, filepath.Join(real, "app.log"), "EXPECTED")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if got, err := readAll(t, filepath.Join(link, "app.log")); err != nil || got != "EXPECTED" {
		t.Fatalf("got %q, %v", got, err)
	}
}
