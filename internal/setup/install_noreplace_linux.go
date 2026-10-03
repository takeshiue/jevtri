//go:build linux && (amd64 || arm64)

package setup

import (
	"runtime"
	"syscall"
	"unsafe"
)

func installNewConfig(temporary, path string) error {
	source, err := syscall.BytePtrFromString(temporary)
	if err != nil {
		return err
	}
	destination, err := syscall.BytePtrFromString(path)
	if err != nil {
		return err
	}
	number := uintptr(316)
	if runtime.GOARCH == "arm64" {
		number = 276
	}
	directory := -100
	// RENAME_NOREPLACE installs the completed file without overwriting a race.
	_, _, errno := syscall.Syscall6(number, uintptr(directory), uintptr(unsafe.Pointer(source)), uintptr(directory), uintptr(unsafe.Pointer(destination)), 1, 0)
	runtime.KeepAlive(source)
	runtime.KeepAlive(destination)
	if errno != 0 {
		return errno
	}
	return nil
}
