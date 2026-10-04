// Package sentlog appends one JSON line per run to the send log, so that
// what left the host can be checked later (spec 12.2). Rotation is left to
// logrotate.
package sentlog

import (
	"encoding/json"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/takeshiue/jevtri/internal/jev"
)

// Record is one line of the send log. It holds the exact request body but
// never the API key or the Authorization header.
type Record struct {
	Time string `json:"time"`
	// Purpose is empty for a ranking and "time_format" for jevtri init.
	Purpose   string         `json:"purpose,omitempty"`
	DryRun    bool           `json:"dry_run"`
	Endpoint  string         `json:"endpoint"`
	Request   any            `json:"request"` // jev.Request or jev.FormatRequest
	Model     string         `json:"model,omitempty"`
	LatencyMS int64          `json:"latency_ms,omitempty"`
	Scores    []recordScore  `json:"scores,omitempty"`
	Options   []recordOption `json:"options,omitempty"`
	Error     string         `json:"error,omitempty"`
}

type recordOption struct {
	Name        string  `json:"name"`
	Probability float64 `json:"probability"`
}

type recordScore struct {
	Log   string  `json:"log"`
	Score float64 `json:"score"`
}

// Entry builds a record for a run.
func Entry(now time.Time, request jev.Request, dryRun bool, result *jev.Result, sendErr error) Record {
	record := Record{Time: now.Format(time.RFC3339Nano), DryRun: dryRun, Endpoint: jev.Endpoint, Request: request}
	if result != nil {
		record.Model = result.Model
		record.LatencyMS = result.Latency.Milliseconds()
		for _, score := range result.Scores {
			record.Scores = append(record.Scores, recordScore{Log: score.Path, Score: score.Raw})
		}
	}
	if sendErr != nil {
		record.Error = sendErr.Error()
	}
	return record
}

// FormatEntry builds a record for a time format question of jevtri init.
func FormatEntry(now time.Time, request jev.FormatRequest, result *jev.FormatResult, sendErr error) Record {
	record := Record{Time: now.Format(time.RFC3339Nano), Purpose: "time_format", Endpoint: jev.Endpoint, Request: request}
	if result != nil {
		record.Model = result.Model
		record.LatencyMS = result.Latency.Milliseconds()
		for _, option := range result.Options {
			record.Options = append(record.Options, recordOption{Name: option.Name, Probability: option.Probability})
		}
	}
	if sendErr != nil {
		record.Error = sendErr.Error()
	}
	return record
}

// Append writes record as one line to path, creating the directory (0700)
// and file (0600) when missing. Existing files with looser permissions are
// tightened, because masking can miss secrets.
func Append(path string, record Record) error {
	line, err := json.Marshal(record)
	if err != nil {
		return err
	}
	handle, err := open(path)
	if err != nil {
		return err
	}
	if _, err := handle.Write(append(line, '\n')); err != nil {
		handle.Close()
		return fmt.Errorf("cannot write the send log %s: %v", path, err)
	}
	// Close reports errors that Write cannot, such as a full disk.
	if err := handle.Close(); err != nil {
		return fmt.Errorf("cannot write the send log %s: %v", path, err)
	}
	return nil
}

// open opens the send log for appending, creating it with 0600 and tightening
// an existing file. O_NOFOLLOW: sent_log comes from the configuration, and a
// link there would make root write the file it points at (SEC-007).
func open(path string) (*os.File, error) {
	handle, err := openPinned(path)
	if err != nil {
		return nil, fmt.Errorf("cannot write the send log %s: %v", path, err)
	}
	info, err := handle.Stat()
	if err != nil {
		handle.Close()
		return nil, fmt.Errorf("cannot write the send log %s: %v", path, err)
	}
	if !info.Mode().IsRegular() {
		handle.Close()
		return nil, fmt.Errorf("the send log %s is not a regular file", path)
	}
	if err := validateFileOwner(info); err != nil {
		handle.Close()
		return nil, fmt.Errorf("cannot write the send log %s: %v", path, err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		if err := handle.Chmod(0o600); err != nil {
			handle.Close()
			return nil, fmt.Errorf("cannot restrict permissions of %s: %v", path, err)
		}
	}
	return handle, nil
}

// CheckWritable makes sure a record can be appended, so that a run does not
// send logs that it cannot record afterwards (spec 12.2).
func CheckWritable(path string) error {
	handle, err := open(path)
	if err != nil {
		return err
	}
	return handle.Close()
}

// chmod does not revoke the owner's access to a pre-existing file.
func validateFileOwner(info os.FileInfo) error {
	metadata, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(metadata.Uid) != os.Geteuid() {
		return fmt.Errorf("send log must be owned by the user running jevtri")
	}
	return nil
}
