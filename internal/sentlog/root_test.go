//go:build roottest

package sentlog

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func requireKernelRoot(t *testing.T) {
	t.Helper()
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(status), "\n") {
		if strings.HasPrefix(line, "Uid:") {
			fields := strings.Fields(line)
			if len(fields) != 5 || fields[1] != "0" || fields[2] != "0" || os.Geteuid() != 0 {
				t.Fatal("roottest requires real kernel root, not a virtual UID")
			}
			return
		}
	}
	t.Fatal("kernel UID evidence unavailable")
}

func TestRootAuditRejectsDifferentUIDBeforeMutation(t *testing.T) {
	requireKernelRoot(t)
	for _, changeParent := range []bool{false, true} {
		parent, err := os.MkdirTemp("/tmp", "jevtri-audit-owner-")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(parent)
		path := filepath.Join(parent, "sent.log")
		if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		ownedPath := path
		if changeParent {
			ownedPath = parent
		}
		if err := os.Chown(ownedPath, 65534, 65534); err != nil {
			t.Fatal(err)
		}
		var owner syscall.Stat_t
		if err := syscall.Stat(ownedPath, &owner); err != nil || owner.Uid != 65534 {
			t.Fatal("real owner change was not established")
		}
		if err := CheckWritable(path); err == nil {
			t.Fatal("other UID accepted by preflight")
		}
		if err := Append(path, Record{Time: "synthetic"}); err == nil {
			t.Fatal("other UID accepted by append")
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "original" {
			t.Fatal("file contents changed")
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o644 {
			t.Fatal("file mode changed")
		}
	}
}

func TestRootAuditSupportsManagedParentSymlink(t *testing.T) {
	requireKernelRoot(t)
	parent, err := os.MkdirTemp("/tmp", "jevtri-audit-link-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(parent)
	target := filepath.Join(parent, "real")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(parent, "link")); err != nil {
		t.Fatal(err)
	}
	if err := Append(filepath.Join(parent, "link", "sent.log"), Record{Time: "synthetic"}); err != nil {
		t.Fatal(err)
	}
}
