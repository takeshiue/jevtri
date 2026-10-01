// Package safeopen opens log files without following symbolic links.
//
// jevtri runs as root and sends what it reads to the Jev API. A log directory
// is often writable by the service that owns it (/var/log/postgresql is
// root:postgres 1775 on Debian), so that service could place a link to
// /etc/shadow where a log is expected, or where a rotated sibling would be,
// and have root read and send it (SEC-006, SEC-007). The same service could
// also swap a subdirectory for a link after jevtri looked at the path, so the
// check has to hold for every element, not only the last one (R-01).
package safeopen

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// ErrSymlink is returned for a path that is a symbolic link.
var ErrSymlink = fmt.Errorf("is a symbolic link; jevtri does not follow links to logs")

// ErrSymlinkParent is returned when a directory on the path is a symbolic
// link placed in a directory that someone other than root can write to.
var ErrSymlinkParent = fmt.Errorf("has a symbolic link in a directory writable by others than root; jevtri does not follow it (set the path to where the link points)")

// maxLinks bounds how many directory links one path may go through, as the
// kernel does with ELOOP.
const maxLinks = 40

// atCurrentDir is AT_FDCWD, which the syscall package does not define on
// every Linux architecture.
const atCurrentDir = -0x64

// Open opens path for reading, refusing symbolic links and anything that is
// not a regular file.
//
// The path is walked one element at a time from directory descriptors, so a
// directory cannot be replaced between the check and the open. The final
// element must not be a link. A directory element may be a link only when
// it sits in a directory that is owned by root and not writable by group or
// others: such a link is root's own arrangement (/var/run to /run), while
// anywhere else it could have been placed by the service writing the logs.
func Open(path string) (*os.File, error) {
	fd, err := walk(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	// Back to blocking reads; for a regular file this changes nothing, but
	// the descriptor then behaves like any other os.File.
	if err := syscall.SetNonblock(fd, false); err != nil {
		syscall.Close(fd)
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	handle := os.NewFile(uintptr(fd), path)
	info, err := handle.Stat()
	if err != nil {
		handle.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		handle.Close()
		return nil, &os.PathError{Op: "open", Path: path, Err: fmt.Errorf("is not a regular file")}
	}
	return handle, nil
}

func walk(path string) (int, error) {
	if path == "" {
		return -1, syscall.ENOENT
	}
	dir, err := startDir(path)
	if err != nil {
		return -1, err
	}
	pending := splitPath(path)
	links := 0
	for {
		if len(pending) == 0 {
			// The path named a directory ("/" or "dir/"); Open rejects it
			// as not a regular file.
			return dir, nil
		}
		name := pending[0]
		pending = pending[1:]
		last := len(pending) == 0
		flags := syscall.O_RDONLY | syscall.O_CLOEXEC | syscall.O_NOFOLLOW
		if !last {
			flags |= syscall.O_DIRECTORY
		} else {
			// Opening a FIFO for reading waits for a writer, so whoever can
			// write the log directory could stop root's jevtri there. Open
			// returns non-regular files as an error without reading them.
			flags |= syscall.O_NONBLOCK
		}
		fd, err := openat(dir, name, flags)
		if err == nil {
			syscall.Close(dir)
			dir = fd
			if last {
				return fd, nil
			}
			continue
		}
		target, isLink := readLink(dir, name)
		if !isLink {
			syscall.Close(dir)
			return -1, err
		}
		if last {
			syscall.Close(dir)
			return -1, ErrSymlink
		}
		if !rootOnly(dir) {
			syscall.Close(dir)
			return -1, ErrSymlinkParent
		}
		links++
		if links > maxLinks {
			syscall.Close(dir)
			return -1, syscall.ELOOP
		}
		if strings.HasPrefix(target, "/") {
			syscall.Close(dir)
			if dir, err = startDir(target); err != nil {
				return -1, err
			}
		}
		pending = append(splitPath(target), pending...)
	}
}

func startDir(path string) (int, error) {
	start := "."
	if strings.HasPrefix(path, "/") {
		start = "/"
	}
	return openat(atCurrentDir, start, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_DIRECTORY)
}

// splitPath keeps ".." as an element: it is opened relative to the directory
// already reached, which is what the kernel does too.
func splitPath(path string) []string {
	var parts []string
	for _, part := range strings.Split(path, "/") {
		if part != "" && part != "." {
			parts = append(parts, part)
		}
	}
	return parts
}

func openat(dir int, name string, flags int) (int, error) {
	for {
		fd, err := syscall.Openat(dir, name, flags, 0)
		if !errors.Is(err, syscall.EINTR) {
			return fd, err
		}
	}
}

// procFD is where the kernel lists this process's descriptors. Without /proc
// no link can be read, so every link is refused. A variable so tests can
// check that.
var procFD = "/proc/self/fd/"

// readLink reads the link name in the directory dir. /proc/self/fd names the
// directory by descriptor, so the lookup stays in the directory already opened.
func readLink(dir int, name string) (string, bool) {
	target, err := os.Readlink(procFD + strconv.Itoa(dir) + "/" + name)
	return target, err == nil
}

// rootOnly reports whether only root can change the entries of dir. It is a
// variable so tests without root can exercise the followed-link path.
var rootOnly = func(dir int) bool {
	var stat syscall.Stat_t
	if err := syscall.Fstat(dir, &stat); err != nil {
		return false
	}
	return stat.Uid == 0 && stat.Mode&0o022 == 0
}

// IsSymlink reports whether path itself is a symbolic link. It is used to drop
// candidates before opening them; Open is what actually protects the read.
func IsSymlink(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode()&os.ModeSymlink != 0
}
