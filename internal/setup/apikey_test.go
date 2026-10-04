package setup

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// IN-10: init starts with the API key: a pasted key is stored with mode
// 0600 and never printed; a bad paste is asked again; Enter skips; an
// existing key is kept unless the user says to replace it.
func TestAskAPIKey(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "etc", "jevtri", "api-key")
	opts := Options{KeyPath: keyPath}
	var out bytes.Buffer

	// A paste with a space is refused, then a good one is stored.
	in := bufio.NewReader(strings.NewReader("bad key\nexample-key-123\n"))
	if err := askAPIKey(in, &out, opts); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(keyPath)
	info, _ := os.Stat(keyPath)
	if err != nil || string(data) != "example-key-123\n" || info.Mode().Perm() != 0o600 {
		t.Fatalf("stored %q mode %v: %v", data, info.Mode().Perm(), err)
	}
	if strings.Contains(out.String(), "example-key-123") || strings.Contains(out.String(), "bad key") {
		t.Errorf("the key was printed:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "has no spaces; try again") {
		t.Errorf("bad paste not explained:\n%s", out.String())
	}

	// An existing key is kept on Enter, replaced on y.
	out.Reset()
	askAPIKey(bufio.NewReader(strings.NewReader("\n")), &out, opts)
	if data, _ := os.ReadFile(keyPath); string(data) != "example-key-123\n" || !strings.Contains(out.String(), "Keeping the current key") {
		t.Errorf("kept: %q %s", data, out.String())
	}
	askAPIKey(bufio.NewReader(strings.NewReader("y\nnew-key-456\n")), &out, opts)
	if data, _ := os.ReadFile(keyPath); string(data) != "new-key-456\n" {
		t.Errorf("replaced: %q", data)
	}

	// Enter alone skips and writes nothing.
	other := Options{KeyPath: filepath.Join(dir, "none", "api-key")}
	out.Reset()
	if err := askAPIKey(bufio.NewReader(strings.NewReader("\n")), &out, other); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(other.KeyPath); err == nil || !strings.Contains(out.String(), "Skipped") {
		t.Errorf("skip wrote a key or said nothing: %v %s", err, out.String())
	}
}

type endlessKeyReader struct{ consumed int }

func (reader *endlessKeyReader) Read(output []byte) (int, error) {
	for index := range output {
		output[index] = 'a'
	}
	reader.consumed += len(output)
	return len(output), nil
}

func TestPastedKeyStopsBeforeUnboundedAllocation(t *testing.T) {
	source := &endlessKeyReader{}
	_, err := readPastedKey(bufio.NewReaderSize(source, 16))
	if err == nil {
		t.Fatal("endless key accepted")
	}
	if source.consumed > maxKeyBytes+32 {
		t.Fatalf("read beyond bound: %d", source.consumed)
	}
	if _, err := readPastedKey(bufio.NewReader(strings.NewReader(strings.Repeat("a", maxKeyBytes+1) + "\n"))); err == nil {
		t.Fatal("oversized line accepted")
	}
	for _, suffix := range []string{"", "\n", "\r\n"} {
		value := strings.Repeat("a", maxKeyBytes)
		key, err := readPastedKey(bufio.NewReader(strings.NewReader(value + suffix)))
		if err != nil || key != value {
			t.Fatalf("valid boundary refused: %v", err)
		}
	}
}

func TestOversizedReplacementKeepsExistingKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api-key")
	if err := os.WriteFile(path, []byte("old-synthetic-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err := askAPIKey(bufio.NewReader(strings.NewReader("y\n"+strings.Repeat("synthetic", 1024)+"\n")), &output, Options{KeyPath: path})
	if err == nil {
		t.Fatal("oversized replacement accepted")
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != "old-synthetic-key\n" {
		t.Fatal("existing key changed")
	}
	if strings.Contains(output.String(), "synthetic") || strings.Contains(err.Error(), "synthetic") {
		t.Fatal("key leaked into message")
	}
}
