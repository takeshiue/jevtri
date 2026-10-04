//go:build linux

package setup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// syscall does not expose O_PATH on every Linux architecture.
const openPathOnly = 0x200000

type deletableLog struct {
	parent int
	name   string
	file   *os.File
	info   os.FileInfo
}

func (d *deletableLog) Info() os.FileInfo { return d.info }
func (d *deletableLog) Close()            { d.file.Close(); syscall.Close(d.parent) }
func openDeletableLog(path string) (*deletableLog, error) {
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") {
		return nil, errors.New("invalid absolute file path")
	}
	parts := strings.Split(strings.TrimPrefix(filepath.Clean(path), "/"), "/")
	if len(parts) == 0 || parts[len(parts)-1] == "" {
		return nil, errors.New("invalid file path")
	}
	dir, err := syscall.Open("/", syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			syscall.Close(dir)
		}
	}()
	for i := 0; i < len(parts)-1; i++ {
		var stat syscall.Stat_t
		if err := syscall.Fstat(dir, &stat); err != nil {
			return nil, err
		}
		trusted := stat.Uid == 0 || int(stat.Uid) == os.Geteuid()
		sticky := stat.Mode&syscall.S_ISVTX != 0 && stat.Uid == 0
		if !trusted || (stat.Mode&0o022 != 0 && !sticky) {
			return nil, errors.New("untrusted or writable parent directory")
		}
		next, err := syscall.Openat(dir, parts[i], syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return nil, err
		}
		syscall.Close(dir)
		dir = next
	}
	var parent syscall.Stat_t
	if err := syscall.Fstat(dir, &parent); err != nil {
		return nil, err
	}
	if (parent.Uid != 0 && int(parent.Uid) != os.Geteuid()) || parent.Mode&0o022 != 0 {
		return nil, errors.New("untrusted or writable final parent directory")
	}
	name := parts[len(parts)-1]
	fd, err := syscall.Openat(dir, name, openPathOnly|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || stat.Nlink != 1 || (stat.Uid != 0 && int(stat.Uid) != os.Geteuid()) {
		file.Close()
		return nil, errors.New("not a regular file with one link")
	}
	success = true
	return &deletableLog{parent: dir, name: name, file: file, info: info}, nil
}
func (d *deletableLog) Delete() error {

	fresh, err := openDeletableLog(d.file.Name())
	if err != nil {
		return err
	}
	defer fresh.Close()
	var pinned, reopened syscall.Stat_t
	if err := syscall.Fstat(d.parent, &pinned); err != nil {
		return err
	}
	if err := syscall.Fstat(fresh.parent, &reopened); err != nil {
		return err
	}
	if pinned.Dev != reopened.Dev || pinned.Ino != reopened.Ino || !os.SameFile(d.info, fresh.info) {
		return errors.New("file path or parent identity changed; deletion refused")
	}
	// A protected parent prevents unprivileged swaps; equal-privilege races remain outside this boundary.
	fd, err := syscall.Openat(d.parent, d.name, openPathOnly|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	current := os.NewFile(uintptr(fd), d.name)
	defer current.Close()
	info, err := current.Stat()
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || stat.Nlink != 1 || (stat.Uid != 0 && int(stat.Uid) != os.Geteuid()) || !os.SameFile(info, d.info) {
		return fmt.Errorf("file identity changed; deletion refused")
	}
	var parent syscall.Stat_t
	if err := syscall.Fstat(d.parent, &parent); err != nil {
		return err
	}
	if (parent.Uid != 0 && int(parent.Uid) != os.Geteuid()) || parent.Mode&0o022 != 0 {
		return errors.New("parent directory permissions changed")
	}
	return syscall.Unlinkat(d.parent, d.name)
}
