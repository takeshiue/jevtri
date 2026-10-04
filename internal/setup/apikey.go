package setup

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/takeshiue/jevtri/internal/config"
)

// maxKeyBytes bounds a pasted key; a longer line is a paste of something else.
const maxKeyBytes = config.MaxAPIKeyBytes

// askAPIKey is the first step of 'jevtri init' (spec 12.7.5): it stores a
// pasted Jev API key in opts.KeyPath as root:root 0600, or keeps the one that
// is there. The pasted key shows on the screen as typed, so that the user can
// check it (2026-10-02 user's decision); jevtri itself never prints it.
// An empty answer skips the step.
func askAPIKey(reader *bufio.Reader, out io.Writer, opts Options) error {
	if opts.KeyPath == "" {
		return nil
	}
	fmt.Fprintf(out, "Jev API key (stored in %s, readable by root only).\n", opts.KeyPath)
	if _, err := config.LoadAPIKey(opts.KeyPath); err == nil {
		answer, ok := ask(reader, out, "  A key is already set. Replace it? [y/N] ")
		if !ok || (answer != "y" && answer != "yes") {
			fmt.Fprintln(out, "  Keeping the current key.")
			return nil
		}
	} else if !errors.Is(err, os.ErrNotExist) && !strings.Contains(err.Error(), "no such file") {
		fmt.Fprintf(out, "  The current key file cannot be used: %v\n", err)
	}
	for {
		fmt.Fprint(out, "  Paste the key and press Enter (Enter alone to skip): ")
		key, err := readPastedKey(reader)
		if err != nil {
			return fmt.Errorf("cannot read the key: %v", err)
		}
		if key == "" {
			fmt.Fprintf(out, "  Skipped. Nothing is sent to Jev until a key is in %s; run 'jevtri init' again to add it.\n", opts.KeyPath)
			return nil
		}
		if problem := keyProblem(key); problem != "" {
			fmt.Fprintf(out, "  %s; try again.\n", problem)
			continue
		}
		if err := writeKey(opts.KeyPath, key); err != nil {
			return err
		}
		if _, err := config.LoadAPIKey(opts.KeyPath); err != nil {
			return fmt.Errorf("the key was written but cannot be used: %v", err)
		}
		fmt.Fprintf(out, "  Saved the key to %s (mode 0600). It was not checked with Jev.\n", opts.KeyPath)
		return nil
	}
}

// Stop on overflow instead of draining a potentially endless pasted line.
func readPastedKey(reader *bufio.Reader) (string, error) {
	line := make([]byte, 0, maxKeyBytes+2)
	for {
		value, err := reader.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return strings.TrimSpace(string(line)), nil
			}
			return "", err
		}
		if value == '\n' {
			return strings.TrimSpace(string(line)), nil
		}
		if len(line) >= maxKeyBytes && (len(line) != maxKeyBytes || value != '\r') {
			return "", fmt.Errorf("that is too long for an API key")
		}
		line = append(line, value)
	}
}

// keyProblem says what is wrong with a pasted key, without repeating it.
func keyProblem(key string) string {
	switch {
	case len(key) > maxKeyBytes:
		return "That is too long for an API key"
	case strings.ContainsAny(key, " \t\r\n"):
		return "An API key has no spaces"
	case strings.ContainsFunc(key, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		return "That contains control characters"
	}
	return ""
}

// writeKey replaces path with key through a temporary 0600 file in the same
// directory, so that the key is never in a file others can read.
func writeKey(path, key string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("cannot create %s: %v", filepath.Dir(path), err)
	}
	return replaceFile(path, key+"\n", 0o600)
}
