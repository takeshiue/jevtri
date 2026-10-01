//go:build adversarial

package safeopen

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// Adversarial checks AD-01 to AD-09 (test plan "例外入力による確認").
// Run as a normal user locally and as root in the distribution containers
// (tests/distro/root.sh with TAGS=adversarial).

// trustOnly makes rootOnly trust exactly the given directories, so tests
// without root can build trusted and untrusted places side by side.
func trustOnly(t *testing.T, dirs ...string) {
	t.Helper()
	saved := rootOnly
	trusted := map[string]bool{}
	for _, dir := range dirs {
		real, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatal(err)
		}
		trusted[real] = true
	}
	rootOnly = func(fd int) bool {
		path, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
		return err == nil && trusted[path]
	}
	t.Cleanup(func() { rootOnly = saved })
}

// untrustedTempDir returns a directory someone other than root may write to,
// also when the test runs as root.
func untrustedTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if os.Geteuid() == 0 {
		if err := os.Chown(dir, 65534, 65534); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func openWithin(t *testing.T, path string, limit time.Duration) (string, error, bool) {
	t.Helper()
	type result struct {
		text string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		handle, err := Open(path)
		if err != nil {
			done <- result{"", err}
			return
		}
		defer handle.Close()
		data, err := io.ReadAll(handle)
		done <- result{string(data), err}
	}()
	select {
	case r := <-done:
		return r.text, r.err, true
	case <-time.After(limit):
		return "", nil, false
	}
}

// AD-01
func TestAD01LinkShapes(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "a", "b"), 0o755)
	trustOnly(t, dir, filepath.Join(dir, "a"))
	mustWrite(t, filepath.Join(dir, "a", "b", "app.log"), "EXPECTED")
	links := map[string]string{
		"self":     "self",
		"dots":     "./a/../a/./b/",
		"slashes":  "a//b///",
		"absolute": filepath.Join(dir, "a", "b"),
		"mixed":    "dots",
		"parent":   "a/b/..",
	}
	for name, target := range links {
		os.Symlink(target, filepath.Join(dir, name))
	}
	for i := 0; i <= maxLinks; i++ {
		target := "a/b"
		if i > 0 {
			target = "chain" + strconv.Itoa(i-1)
		}
		os.Symlink(target, filepath.Join(dir, "chain"+strconv.Itoa(i)))
	}
	cases := []struct {
		path string
		loop bool
	}{
		{"self/app.log", true},
		{"dots/app.log", false},
		{"slashes/app.log", false},
		{"absolute/app.log", false},
		{"mixed/app.log", false},
		{"parent/b/app.log", false},
		{"chain" + strconv.Itoa(maxLinks-1) + "/app.log", false},
		{"chain" + strconv.Itoa(maxLinks) + "/app.log", true},
	}
	for _, c := range cases {
		path := filepath.Join(dir, c.path)
		got, err, finished := openWithin(t, path, 5*time.Second)
		if !finished {
			t.Errorf("%s: did not return", c.path)
			continue
		}
		if c.loop {
			if !errors.Is(err, syscall.ELOOP) {
				t.Errorf("%s: err = %v, want ELOOP", c.path, err)
			}
			continue
		}
		want, evalErr := filepath.EvalSymlinks(path)
		if evalErr != nil || err != nil || got != "EXPECTED" {
			t.Errorf("%s: got %q, %v (kernel resolves to %s, %v)", c.path, got, err, want, evalErr)
		}
	}
}

// AD-02: trusted link into an untrusted directory whose child is a link.
func TestAD02TrustEndsAtUntrustedDirectory(t *testing.T) {
	trustedDir := t.TempDir()
	untrusted := untrustedTempDir(t)
	trustOnly(t, trustedDir)
	os.Mkdir(filepath.Join(untrusted, "secret"), 0o755)
	mustWrite(t, filepath.Join(untrusted, "secret", "app.log"), "SECRET")
	os.Symlink(filepath.Join(untrusted, "secret"), filepath.Join(untrusted, "logs"))
	os.Symlink(untrusted, filepath.Join(trustedDir, "service"))
	_, err, _ := openWithin(t, filepath.Join(trustedDir, "service", "logs", "app.log"), 5*time.Second)
	if !errors.Is(err, ErrSymlinkParent) {
		t.Errorf("err = %v, want ErrSymlinkParent", err)
	}
}

// AD-03: a writer flips a directory between real and link while Open runs.
func TestAD03SwapRace(t *testing.T) {
	dir := untrustedTempDir(t)
	sub := filepath.Join(dir, "sub")
	os.Mkdir(sub, 0o755)
	mustWrite(t, filepath.Join(sub, "app.log"), "EXPECTED")
	os.Mkdir(filepath.Join(dir, "alt"), 0o755)
	mustWrite(t, filepath.Join(dir, "alt", "app.log"), "SECRET")
	var stop atomic.Bool
	var flips atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		for !stop.Load() {
			os.Rename(sub, sub+".real")
			os.Symlink(filepath.Join(dir, "alt"), sub)
			os.Remove(sub)
			os.Rename(sub+".real", sub)
			flips.Add(1)
		}
	}()
	counts := map[string]int{}
	for i := 0; i < 10000; i++ {
		handle, err := Open(filepath.Join(sub, "app.log"))
		if err != nil {
			// While sub is renamed away the path does not exist for a moment.
			key := "error: " + err.Error()
			if errors.Is(err, ErrSymlinkParent) {
				key = "refused"
			} else if errors.Is(err, os.ErrNotExist) {
				key = "missing"
			} else if errors.Is(err, syscall.ENOTDIR) {
				// O_NOFOLLOW met the link, which was gone again before Open
				// could tell it was one: still a refusal.
				key = "refused"
			}
			counts[key]++
			continue
		}
		data, err := io.ReadAll(handle)
		handle.Close()
		if err != nil {
			counts["read error: "+err.Error()]++
			continue
		}
		counts[string(data)]++
	}
	stop.Store(true)
	<-done
	t.Logf("results %v, flips %d", counts, flips.Load())
	// The check only means something if it saw both outcomes of the race:
	// a refusal proves the link state was hit, a normal read proves Open
	// still works while the directory flips.
	for key, count := range counts {
		switch key {
		case "EXPECTED", "refused", "missing":
		case "SECRET":
			t.Errorf("read the other file %d times", count)
		default:
			t.Errorf("unexpected result %q %d times", key, count)
		}
	}
	if counts["EXPECTED"] == 0 {
		t.Error("no normal read succeeded; the check cannot tell a safe Open from a broken one")
	}
	if counts["refused"] == 0 {
		t.Error("the race never hit the link state; nothing was checked")
	}
}

// AD-04: without /proc no link can be read, so links are refused.
func TestAD04NoProc(t *testing.T) {
	dir := t.TempDir()
	trustOnly(t, dir)
	os.Mkdir(filepath.Join(dir, "real"), 0o755)
	mustWrite(t, filepath.Join(dir, "real", "app.log"), "EXPECTED")
	os.Symlink("real", filepath.Join(dir, "link"))
	saved := procFD
	procFD = "/nonexistent-proc/self/fd/"
	defer func() { procFD = saved }()
	if _, err, _ := openWithin(t, filepath.Join(dir, "link", "app.log"), 5*time.Second); err == nil {
		t.Error("a link was followed without /proc")
	}
	if got, err, _ := openWithin(t, filepath.Join(dir, "real", "app.log"), 5*time.Second); err != nil || got != "EXPECTED" {
		t.Errorf("plain path: %q, %v", got, err)
	}
}

// AD-05: special files where a log is expected must not hang or be read.
func TestAD05SpecialFiles(t *testing.T) {
	dir := untrustedTempDir(t)
	fifo := filepath.Join(dir, "fifo.log")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "socket.log")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	os.Mkdir(filepath.Join(dir, "dir.log"), 0o755)
	for _, path := range []string{fifo, socket, "/dev/null", "/dev/zero", filepath.Join(dir, "dir.log")} {
		_, err, finished := openWithin(t, path, 5*time.Second)
		if !finished {
			t.Errorf("%s: Open did not return within 5s", path)
			continue
		}
		if err == nil {
			t.Errorf("%s: opened as a log", path)
		}
	}
}

// AD-06: directories that look safe by owner alone but are writable by others.
func TestAD06UntrustedModes(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to create root-owned directories")
	}
	base := t.TempDir() // root-owned 0700: trusted
	cases := map[string]func(string) error{
		"sticky 1777": func(d string) error { return os.Chmod(d, 0o1777) },
		"owner nobody 0755": func(d string) error {
			return os.Chown(d, 65534, 65534)
		},
		"group writable 0775": func(d string) error {
			if err := os.Chmod(d, 0o775); err != nil {
				return err
			}
			return os.Chown(d, 0, 4)
		},
		"ACL user nobody rwx on 0755": func(d string) error { return setUserACL(d, 65534) },
	}
	for name, prepare := range cases {
		dir := filepath.Join(base, strings.ReplaceAll(name, " ", "_"))
		os.Mkdir(dir, 0o755)
		os.Mkdir(filepath.Join(dir, "real"), 0o755)
		mustWrite(t, filepath.Join(dir, "real", "app.log"), "SECRET")
		os.Symlink("real", filepath.Join(dir, "link"))
		if err := prepare(dir); err != nil {
			t.Errorf("%s: prepare: %v", name, err)
			continue
		}
		var stat syscall.Stat_t
		syscall.Stat(dir, &stat)
		_, err, _ := openWithin(t, filepath.Join(dir, "link", "app.log"), 5*time.Second)
		if !errors.Is(err, ErrSymlinkParent) {
			t.Errorf("%s (uid %d mode %o): err = %v, want ErrSymlinkParent", name, stat.Uid, stat.Mode&0o7777, err)
		}
	}
}

// setUserACL grants uid rwx on dir through a POSIX ACL, written as the
// system.posix_acl_access attribute so no setfacl is needed.
func setUserACL(dir string, uid uint32) error {
	type entry struct {
		tag, perm uint16
		id        uint32
	}
	const undefined = 0xffffffff
	entries := []entry{
		{0x01, 7, undefined}, // user::rwx
		{0x02, 7, uid},       // user:uid:rwx
		{0x04, 5, undefined}, // group::r-x
		{0x10, 7, undefined}, // mask::rwx
		{0x20, 5, undefined}, // other::r-x
	}
	buf := make([]byte, 4+8*len(entries))
	binary.LittleEndian.PutUint32(buf, 2)
	for i, e := range entries {
		binary.LittleEndian.PutUint16(buf[4+8*i:], e.tag)
		binary.LittleEndian.PutUint16(buf[6+8*i:], e.perm)
		binary.LittleEndian.PutUint32(buf[8+8*i:], e.id)
	}
	return syscall.Setxattr(dir, "system.posix_acl_access", buf, 0)
}

// AD-07: Ubuntu's /var/log is root:syslog 0775. A link there is refused
// with a message naming the fix, and plain logs in /var/log still open.
func TestAD07UbuntuVarLog(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	var saved syscall.Stat_t
	if err := syscall.Stat("/var/log", &saved); err != nil {
		t.Fatal(err)
	}
	defer func() {
		os.Chmod("/var/log", os.FileMode(saved.Mode&0o777))
		os.Chown("/var/log", int(saved.Uid), int(saved.Gid))
	}()
	os.Chown("/var/log", 0, 4)
	os.Chmod("/var/log", 0o775)
	real := "/var/log/jevtri-ad07-real"
	link := "/var/log/jevtri-ad07"
	os.RemoveAll(real)
	os.Remove(link)
	defer os.RemoveAll(real)
	defer os.Remove(link)
	os.Mkdir(real, 0o755)
	mustWrite(t, filepath.Join(real, "app.log"), "EXPECTED")
	os.Symlink(real, link)
	// The syslog group could swap this link, so it is refused by design
	// (decided 2026-10-01); the error tells to configure the real path.
	_, err, _ := openWithin(t, filepath.Join(link, "app.log"), 5*time.Second)
	if !errors.Is(err, ErrSymlinkParent) {
		t.Errorf("link in root:4 0775 /var/log: err = %v, want ErrSymlinkParent", err)
	}
	// A log directly in /var/log, the common case, must still open.
	mustWrite(t, "/var/log/jevtri-ad07.log", "PLAIN")
	defer os.Remove("/var/log/jevtri-ad07.log")
	if got, err, _ := openWithin(t, "/var/log/jevtri-ad07.log", 5*time.Second); err != nil || got != "PLAIN" {
		t.Errorf("plain log in /var/log: got %q, %v", got, err)
	}
}

// AD-08: odd paths return an error or the right file, never hang or panic.
func TestAD08OddPaths(t *testing.T) {
	dir := t.TempDir()
	odd := []string{"new\nline.log", "tab\tand\x1b[31mesc.log", "bad\xff\xfeutf8.log", strings.Repeat("n", 255)}
	for _, name := range odd {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("ODD"), 0o600); err != nil {
			t.Logf("%.40q: this file system cannot create it: %v", name, err)
			continue
		}
		if got, err, _ := openWithin(t, path, 5*time.Second); err != nil || got != "ODD" {
			t.Errorf("%q: got %q, %v", name, got, err)
		}
	}
	bad := []string{"", "/", strings.Repeat("n", 256), "/" + strings.Repeat("d/", 2100) + "x", dir + "/missing/app.log", dir + "/\x00nul"}
	for _, path := range bad {
		_, err, finished := openWithin(t, path, 5*time.Second)
		if !finished {
			t.Errorf("%.40q: did not return", path)
		} else if err == nil {
			t.Errorf("%.40q: opened", path)
		} else {
			t.Logf("%.40q: %.120v", path, err)
		}
	}
}

// AD-09: failing paths must not leak descriptors.
func TestAD09NoDescriptorLeak(t *testing.T) {
	dir := untrustedTempDir(t)
	os.Mkdir(filepath.Join(dir, "real"), 0o755)
	mustWrite(t, filepath.Join(dir, "real", "app.log"), "X")
	os.Symlink("real", filepath.Join(dir, "link"))
	os.Symlink(filepath.Join(dir, "real", "app.log"), filepath.Join(dir, "final.log"))
	os.Symlink("loop", filepath.Join(dir, "loop"))
	count := func() int {
		entries, _ := os.ReadDir("/proc/self/fd")
		return len(entries)
	}
	before := count()
	paths := []string{"link/app.log", "final.log", "loop/x", "missing/x", "real", "real/app.log/x"}
	for i := 0; i < 10000; i++ {
		handle, err := Open(filepath.Join(dir, paths[i%len(paths)]))
		if err == nil {
			handle.Close()
		}
	}
	if after := count(); after > before {
		t.Errorf("descriptors grew from %d to %d", before, after)
	}
}

var _ = fmt.Sprint
