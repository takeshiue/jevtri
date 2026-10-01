package safeopen

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readAll(t *testing.T, path string) (string, error) {
	t.Helper()
	handle, err := Open(path)
	if err != nil {
		return "", err
	}
	defer handle.Close()
	data, err := io.ReadAll(handle)
	if err != nil {
		t.Fatal(err)
	}
	return string(data), nil
}

func TestOpensRegularFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "app.log")
	os.Mkdir(filepath.Dir(path), 0o755)
	mustWrite(t, path, "EXPECTED")
	if got, err := readAll(t, path); err != nil || got != "EXPECTED" {
		t.Fatalf("got %q, %v", got, err)
	}
	// Relative paths and ".." resolve as the kernel does.
	wd, _ := os.Getwd()
	defer os.Chdir(wd)
	os.Chdir(dir)
	if got, err := readAll(t, "sub/../sub/./app.log"); err != nil || got != "EXPECTED" {
		t.Fatalf("relative: got %q, %v", got, err)
	}
}

func TestRefusesFinalLink(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "secret"), "SECRET")
	os.Symlink(filepath.Join(dir, "secret"), filepath.Join(dir, "app.log"))
	if _, err := readAll(t, filepath.Join(dir, "app.log")); !errors.Is(err, ErrSymlink) {
		t.Fatalf("err = %v, want ErrSymlink", err)
	}
}

func TestRefusesDirectory(t *testing.T) {
	if _, err := readAll(t, t.TempDir()); err == nil {
		t.Fatal("a directory was opened as a log")
	}
}

// R-01: the parent is swapped for a link after the caller checked the path.
// The temporary directory is not root-only, so the link must be refused.
func TestRefusesSwappedParent(t *testing.T) {
	dir := t.TempDir()
	if os.Geteuid() == 0 {
		// As root the temporary directory would be root-only and trusted;
		// hand it to nobody to keep it writable by someone other than root.
		if err := os.Chown(dir, 65534, 65534); err != nil {
			t.Fatal(err)
		}
	}
	parent := filepath.Join(dir, "parent")
	alternate := filepath.Join(dir, "alternate")
	os.Mkdir(parent, 0o755)
	os.Mkdir(alternate, 0o755)
	mustWrite(t, filepath.Join(parent, "app.log"), "EXPECTED")
	mustWrite(t, filepath.Join(alternate, "app.log"), "ALTERNATE")
	path := filepath.Join(parent, "app.log")
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(parent, parent+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(alternate, parent); err != nil {
		t.Fatal(err)
	}
	got, err := readAll(t, path)
	if !errors.Is(err, ErrSymlinkParent) {
		t.Fatalf("got %q, err = %v, want ErrSymlinkParent", got, err)
	}
}

// A directory link that root placed in a root-only directory is followed.
func TestFollowsRootOwnedDirectoryLink(t *testing.T) {
	info, err := os.Lstat("/var/run")
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Skip("/var/run is not a link here")
	}
	var stat syscall.Stat_t
	if err := syscall.Stat("/var", &stat); err != nil || stat.Uid != 0 || stat.Mode&0o022 != 0 {
		t.Skip("/var is not root-only here")
	}
	target, err := filepath.EvalSymlinks("/var/run")
	if err != nil {
		t.Skip(err)
	}
	entries, _ := os.ReadDir(target)
	for _, entry := range entries {
		if entry.Type().IsRegular() {
			path := filepath.Join("/var/run", entry.Name())
			handle, err := Open(path)
			if errors.Is(err, ErrSymlinkParent) {
				t.Fatalf("%s: %v", path, err)
			}
			if err == nil {
				handle.Close()
			}
			return
		}
	}
	t.Skip("no regular file under /var/run")
}

// With the directory trusted, directory links (absolute and relative, in a
// chain) are followed while the final element is still refused as a link.
func TestFollowsLinkInTrustedDirectory(t *testing.T) {
	saved := rootOnly
	rootOnly = func(int) bool { return true }
	defer func() { rootOnly = saved }()
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "real"), 0o755)
	mustWrite(t, filepath.Join(dir, "real", "app.log"), "EXPECTED")
	os.Symlink(filepath.Join(dir, "real"), filepath.Join(dir, "absolute"))
	os.Symlink("absolute", filepath.Join(dir, "relative"))
	if got, err := readAll(t, filepath.Join(dir, "relative", "app.log")); err != nil || got != "EXPECTED" {
		t.Fatalf("got %q, %v", got, err)
	}
	os.Symlink(filepath.Join(dir, "real", "app.log"), filepath.Join(dir, "real", "link.log"))
	if _, err := readAll(t, filepath.Join(dir, "relative", "link.log")); !errors.Is(err, ErrSymlink) {
		t.Fatalf("err = %v, want ErrSymlink", err)
	}
	os.Symlink("loop", filepath.Join(dir, "loop"))
	if _, err := readAll(t, filepath.Join(dir, "loop", "app.log")); !errors.Is(err, syscall.ELOOP) {
		t.Fatalf("err = %v, want ELOOP", err)
	}
}

// AD-05: a FIFO where a log is expected is refused at once instead of
// waiting for a writer.
func TestRefusesFIFOWithoutWaiting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fifo.log")
	if err := syscall.Mkfifo(path, 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		handle, err := Open(path)
		if err == nil {
			handle.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a FIFO was opened as a log")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Open waited on the FIFO")
	}
}
