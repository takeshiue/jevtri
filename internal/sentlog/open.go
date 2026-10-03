package sentlog

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

const atCurrentDirectory = -0x64

// Descriptor-relative traversal prevents an attacker swapping a checked parent.
func openPinned(path string) (*os.File, error) {
	if path == "" || strings.HasSuffix(path, "/") {
		return nil, syscall.EISDIR
	}
	directory, err := initialDirectory(path)
	if err != nil {
		return nil, err
	}
	defer func() { syscall.Close(directory) }()
	pending := pathElements(path)
	links := 0
	for len(pending) > 0 {
		name := pending[0]
		pending = pending[1:]
		if len(pending) == 0 {
			// O_NONBLOCK prevents a FIFO without a reader from stopping preflight.
			fd, err := openAt(directory, name, syscall.O_WRONLY|syscall.O_APPEND|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
			if err != nil {
				return nil, err
			}
			return os.NewFile(uintptr(fd), path), nil
		}
		flags := syscall.O_RDONLY | syscall.O_DIRECTORY | syscall.O_CLOEXEC | syscall.O_NOFOLLOW
		fd, err := openAt(directory, name, flags, 0)
		if errors.Is(err, syscall.ENOENT) {
			if err = syscall.Mkdirat(directory, name, 0o700); err == nil || errors.Is(err, syscall.EEXIST) {
				fd, err = openAt(directory, name, flags, 0)
			}
		}
		if err == nil {
			syscall.Close(directory)
			directory = fd
			continue
		}
		target, linkErr := os.Readlink("/proc/self/fd/" + strconv.Itoa(directory) + "/" + name)
		if linkErr != nil {
			return nil, err
		}
		if !trustedLinkDirectory(directory) {
			return nil, fmt.Errorf("send log parent link is in a directory writable by others than root")
		}
		links++
		if links > 40 {
			return nil, syscall.ELOOP
		}
		if strings.HasPrefix(target, "/") {
			fd, err = initialDirectory(target)
			if err != nil {
				return nil, err
			}
			syscall.Close(directory)
			directory = fd
		}
		pending = append(pathElements(target), pending...)
	}
	return nil, syscall.EISDIR
}

func initialDirectory(path string) (int, error) {
	start := "."
	if strings.HasPrefix(path, "/") {
		start = "/"
	}
	return openAt(atCurrentDirectory, start, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_DIRECTORY, 0)
}

func pathElements(path string) []string {
	var elements []string
	for _, element := range strings.Split(path, "/") {
		if element != "" && element != "." {
			elements = append(elements, element)
		}
	}
	return elements
}

func openAt(directory int, name string, flags int, mode uint32) (int, error) {
	for {
		fd, err := syscall.Openat(directory, name, flags, mode)
		if !errors.Is(err, syscall.EINTR) {
			return fd, err
		}
	}
}

// This matches safeopen: root's own links such as /var/run remain supported.
var trustedLinkDirectory = func(directory int) bool {
	var stat syscall.Stat_t
	if syscall.Fstat(directory, &stat) != nil {
		return false
	}
	return stat.Uid == 0 && stat.Mode&0o022 == 0
}
