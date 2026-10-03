package sentlog

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestFIFOIsRejectedWithoutWaitingForReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- CheckWritable(path) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO accepted")
		}
	case <-time.After(time.Second):
		// Opening a reader releases a regressed blocking writer so the test can exit.
		fd, _ := syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK, 0)
		if fd >= 0 {
			syscall.Close(fd)
		}
		t.Fatal("send log preflight blocked on FIFO")
	}
	if err := Append(path, Record{Time: "x"}); err == nil {
		t.Fatal("FIFO append accepted")
	}
}

func TestUntrustedParentSymlinkCannotCreateOrModifyTarget(t *testing.T) {
	parent, target := t.TempDir(), t.TempDir()
	if err := os.Chmod(parent, 0o777); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	targetFile := filepath.Join(target, "sent.log")
	for _, existing := range []bool{false, true} {
		if existing {
			if err := os.WriteFile(targetFile, []byte("original"), 0o644); err != nil {
				t.Fatal(err)
			}
			os.Chmod(targetFile, 0o644)
		}
		path := filepath.Join(link, "sent.log")
		if err := CheckWritable(path); err == nil {
			t.Fatal("untrusted parent accepted by preflight")
		}
		if err := Append(path, Record{Time: "x"}); err == nil {
			t.Fatal("untrusted parent accepted by append")
		}
		if existing {
			data, err := os.ReadFile(targetFile)
			if err != nil || string(data) != "original" {
				t.Fatalf("target changed: %q %v", data, err)
			}
			info, _ := os.Stat(targetFile)
			if info.Mode().Perm() != 0o644 {
				t.Fatal("target permissions changed")
			}
		} else if _, err := os.Stat(targetFile); !os.IsNotExist(err) {
			t.Fatalf("target created: %v", err)
		}
	}
}

func TestTrustedParentSymlinkSupportsRelativeAndAbsoluteTargets(t *testing.T) {
	original := trustedLinkDirectory
	trustedLinkDirectory = func(int) bool { return true }
	defer func() { trustedLinkDirectory = original }()
	for _, absolute := range []bool{false, true} {
		parent := t.TempDir()
		target := filepath.Join(parent, "real")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		linkTarget := "real"
		if absolute {
			linkTarget = target
		}
		if err := os.Symlink(linkTarget, filepath.Join(parent, "link")); err != nil {
			t.Fatal(err)
		}
		if err := Append(filepath.Join(parent, "link", "nested", "sent.log"), Record{Time: "x"}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(target, "nested", "sent.log")); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFinalSymlinkAndParentLinkLoopsAreRejected(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "sent.log")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := Append(link, Record{Time: "x"}); err == nil {
		t.Fatal("final symlink accepted")
	}
	original := trustedLinkDirectory
	trustedLinkDirectory = func(int) bool { return true }
	defer func() { trustedLinkDirectory = original }()
	if err := os.Symlink("loop", filepath.Join(parent, "loop")); err != nil {
		t.Fatal(err)
	}
	if err := CheckWritable(filepath.Join(parent, "loop", "sent.log")); err == nil {
		t.Fatal("link loop accepted")
	}
}
