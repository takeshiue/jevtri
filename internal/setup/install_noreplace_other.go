//go:build !linux || (!amd64 && !arm64)

package setup

import "os"

func installNewConfig(temporary, path string) error {
	return os.Link(temporary, path)
}
