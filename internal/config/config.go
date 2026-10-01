// Package config reads /etc/jevtri/jevtri.conf (an INI-like file) and the
// API key file.
//
// Format:
//
//	# comment
//	[general]
//	minutes = 5
//	max_bytes = 48000
//	sent_log = /var/log/jevtri/sent.log
//
//	[log nginx-error]
//	path = /var/log/nginx/error.log
//	time_format = slash-ymd
//	timezone = Asia/Tokyo
//	read_compressed = yes
//	mask = order-\d+
//
// mask may be given several times.
package config

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/takeshiue/jevtri/internal/budget"
	"github.com/takeshiue/jevtri/internal/timefmt"
	"github.com/takeshiue/jevtri/internal/window"
)

// Default locations.
const (
	DefaultPath       = "/etc/jevtri/jevtri.conf"
	DefaultAPIKeyPath = "/etc/jevtri/api-key"
	DefaultSentLog    = "/var/log/jevtri/sent.log"
)

// Config is the parsed configuration.
type Config struct {
	Minutes  int
	MaxBytes int
	SentLog  string
	Logs     []Log
}

// Log is one [log name] section.
type Log struct {
	Name           string
	Path           string
	TimeFormat     string // empty until detected by jevtri init
	Format         *timefmt.Format
	Timezone       string
	Location       *time.Location
	ReadCompressed bool
	Masks          []string
	Line           int // line of the section header, for messages
}

// Error points at a line of the configuration file.
type Error struct {
	File string
	Line int
	Msg  string
}

func (e *Error) Error() string { return fmt.Sprintf("%s:%d: %s", e.File, e.Line, e.Msg) }

// Load reads and validates the file at path. jevtri runs as root and both
// reads the files named here and writes the send log, so a configuration that
// another user can write is refused (SEC-006).
func Load(path string) (*Config, error) {
	handle, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	info, err := handle.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if err := refuseIfOthersCanWrite(path, info, "configuration file"); err != nil {
		return nil, err
	}
	return Parse(handle, path)
}

// refuseIfOthersCanWrite fails when info is writable by its group or by other
// users, or is owned by a user other than the one running jevtri.
func refuseIfOthersCanWrite(path string, info os.FileInfo, what string) error {
	if mode := info.Mode().Perm(); mode&0o022 != 0 {
		return fmt.Errorf("the %s %s is writable by other users (mode %04o); run: chmod go-w %s", what, path, mode, path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil // not a Unix file system: the mode check above is all there is
	}
	if uid := os.Geteuid(); int(stat.Uid) != uid {
		return fmt.Errorf("the %s %s is owned by uid %d, not by uid %d which is running jevtri; run: chown %d %s", what, path, stat.Uid, uid, uid, path)
	}
	return nil
}

// Parse reads a configuration. name is used in error messages.
func Parse(reader io.Reader, name string) (*Config, error) {
	cfg := &Config{Minutes: 5, MaxBytes: budget.DefaultLimit, SentLog: DefaultSentLog}
	var current *Log
	section := ""
	seenNames := map[string]int{}
	scanner := bufio.NewScanner(reader)
	number := 0
	fail := func(format string, args ...any) error {
		return &Error{File: name, Line: number, Msg: fmt.Sprintf(format, args...)}
	}
	for scanner.Scan() {
		number++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				return nil, fail("section header must end with ]")
			}
			header := strings.Fields(strings.TrimSpace(line[1 : len(line)-1]))
			switch {
			case len(header) == 1 && header[0] == "general":
				section, current = "general", nil
			case len(header) == 2 && header[0] == "log":
				if first, ok := seenNames[header[1]]; ok {
					return nil, fail("log %q is already defined at line %d", header[1], first)
				}
				seenNames[header[1]] = number
				cfg.Logs = append(cfg.Logs, Log{Name: header[1], Line: number})
				section, current = "log", &cfg.Logs[len(cfg.Logs)-1]
			default:
				return nil, fail("unknown section %s; use [general] or [log NAME]", line)
			}
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fail("expected key = value")
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch section {
		case "general":
			if err := setGeneral(cfg, key, value); err != nil {
				return nil, fail("%v", err)
			}
		case "log":
			if err := setLog(current, key, value); err != nil {
				return nil, fail("%v", err)
			}
		default:
			return nil, fail("%s is outside a section", key)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	for i := range cfg.Logs {
		if err := finishLog(&cfg.Logs[i]); err != nil {
			return nil, &Error{File: name, Line: cfg.Logs[i].Line, Msg: err.Error()}
		}
	}
	return cfg, nil
}

func setGeneral(cfg *Config, key, value string) error {
	switch key {
	case "minutes":
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 || n > window.MaxMinutes {
			return fmt.Errorf("minutes must be a positive integer up to %d", window.MaxMinutes)
		}
		cfg.Minutes = n
	case "max_bytes":
		n, err := strconv.Atoi(value)
		if err != nil || n < 1000 {
			return fmt.Errorf("max_bytes must be an integer of at least 1000")
		}
		cfg.MaxBytes = n
	case "sent_log":
		if !strings.HasPrefix(value, "/") {
			return fmt.Errorf("sent_log must be an absolute path")
		}
		cfg.SentLog = value
	default:
		return fmt.Errorf("unknown key %q in [general]; known keys: minutes, max_bytes, sent_log", key)
	}
	return nil
}

func setLog(log *Log, key, value string) error {
	switch key {
	case "path":
		if !strings.HasPrefix(value, "/") {
			return fmt.Errorf("path must be an absolute path")
		}
		log.Path = value
	case "time_format":
		log.TimeFormat = value
	case "timezone":
		log.Timezone = value
	case "read_compressed":
		switch strings.ToLower(value) {
		case "yes", "true", "on", "1":
			log.ReadCompressed = true
		case "no", "false", "off", "0":
			log.ReadCompressed = false
		default:
			return fmt.Errorf("read_compressed must be yes or no")
		}
	case "mask":
		log.Masks = append(log.Masks, value)
	default:
		return fmt.Errorf("unknown key %q in [log %s]; known keys: path, time_format, timezone, read_compressed, mask", key, log.Name)
	}
	return nil
}

func finishLog(log *Log) error {
	if log.Path == "" {
		return fmt.Errorf("[log %s] has no path", log.Name)
	}
	if log.TimeFormat != "" {
		format, err := timefmt.Resolve(log.TimeFormat)
		if err != nil {
			return fmt.Errorf("[log %s]: %v", log.Name, err)
		}
		log.Format = format
	}
	log.Location = time.Local
	if log.Timezone != "" {
		location, err := time.LoadLocation(log.Timezone)
		if err != nil {
			return fmt.Errorf("[log %s]: unknown timezone %q", log.Name, log.Timezone)
		}
		log.Location = location
	}
	return nil
}

// LoadAPIKey reads the key file, refusing files that other users can read
// (spec: the key file must be 0600 and must not be empty).
func LoadAPIKey(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("cannot read the API key file %s: %v", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("the API key file %s is not a regular file", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("the API key file %s is readable by other users (mode %04o); run: chmod 600 %s", path, info.Mode().Perm(), path)
	}
	// Someone else's key would send the logs to their Jev account (spec O-08).
	if err := refuseIfOthersCanWrite(path, info, "API key file"); err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("cannot read the API key file %s: %v", path, err)
	}
	key := strings.TrimSpace(string(data))
	if key == "" || strings.ContainsAny(key, " \t\r\n") {
		return "", fmt.Errorf("the API key file %s must contain exactly one key on one line", path)
	}
	return key, nil
}
