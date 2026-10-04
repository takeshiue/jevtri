//go:build !linux

package setup

import (
	"errors"
	"os"
)

type deletableLog struct{}

func openDeletableLog(string) (*deletableLog, error) {
	return nil, errors.New("safe log deletion is supported only on Linux")
}
func (*deletableLog) Info() os.FileInfo { return nil }
func (*deletableLog) Close()            {}
func (*deletableLog) Delete() error {
	return errors.New("safe log deletion is supported only on Linux")
}
