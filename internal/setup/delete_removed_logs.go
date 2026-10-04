package setup

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/takeshiue/jevtri/internal/config"
	"github.com/takeshiue/jevtri/internal/docker"
	"github.com/takeshiue/jevtri/internal/printsafe"
)

func deleteRemovedHostLogs(reader *bufio.Reader, out io.Writer, original *config.Config, explicitlyRemoved []string, inventory []docker.Container, discoveryError error, opts Options) {
	if len(explicitlyRemoved) == 0 {
		return
	}
	current, err := config.Load(opts.ConfigPath)
	if err != nil {
		fmt.Fprintln(out, "Warning: registrations were removed, but saved settings could not be checked; log files were kept.")
		return
	}
	wanted := map[string]bool{}
	for _, name := range explicitlyRemoved {
		wanted[name] = true
	}
	protected := []string{opts.ConfigPath, filepath.Join(opts.Root, config.DefaultPath), filepath.Join(opts.Root, config.DefaultSentLog), filepath.Join(opts.Root, current.SentLog), filepath.Join(opts.Root, original.SentLog), filepath.Join(opts.Root, opts.SentLog), filepath.Join(opts.Root, config.DefaultAPIKeyPath)}
	if opts.KeyPath != "" {
		protected = append(protected, opts.KeyPath)
	}
	for _, entry := range current.Logs {
		if entry.Path != "" {
			protected = append(protected, filepath.Join(opts.Root, entry.Path))
		}
	}
	for _, pattern := range append([]string(nil), protected...) {
		if paths, err := filepath.Glob(pattern); err == nil {
			protected = append(protected, paths...)
		}
	}
	seen := map[string]bool{}
	for _, entry := range original.Logs {
		if !wanted[entry.Name] || entry.Path == "" {
			continue
		}
		path := filepath.Clean(filepath.Join(opts.Root, entry.Path))
		if seen[path] {
			continue
		}
		seen[path] = true
		reason := ""
		switch {
		case entry.JournalUnit != "":
			reason = "journald is not a deletable log file"
		case entry.DockerContainer != "" || entry.DockerProject != "" || dockerManagedPath(entry.Path, inventory):
			reason = "Docker-managed logs must be cleaned up with Docker, not by jevtri"
		case discoveryError != nil:
			reason = "Docker state is unknown; file deletion is refused"
		case strings.ContainsAny(entry.Path, "*?["):
			reason = "log patterns are not deleted"
		}
		for _, container := range inventory {
			if container.LogPath != "" && filepath.Clean(container.LogPath) == filepath.Clean(entry.Path) {
				reason = "Docker-managed logs must be cleaned up with Docker, not by jevtri"
			}
		}
		if reason != "" {
			fmt.Fprintf(out, "Kept log file %s: %s. Registration was removed.\n", printsafe.Line(entry.Path), reason)
			continue
		}
		target, openError := openDeletableLog(path)
		if openError != nil {
			fmt.Fprintf(out, "Kept log file %s: unsafe or unavailable file (%s). Registration was removed.\n", printsafe.Line(entry.Path), printsafe.Line(openError.Error()))
			continue
		}
		blocked := false
		for _, other := range protected {
			matches, _ := filepath.Match(other, path)
			rotatedDot, _ := filepath.Match(other+".*", path)
			rotatedDash, _ := filepath.Match(other+"-*", path)
			if filepath.Clean(other) == path || matches || rotatedDot || rotatedDash {
				blocked = true
				break
			}
			if info, statError := os.Stat(other); statError == nil && os.SameFile(info, target.Info()) {
				blocked = true
				break
			}
		}
		if blocked {
			target.Close()
			fmt.Fprintf(out, "Kept log file %s: still registered or protected. Registration was removed.\n", printsafe.Line(entry.Path))
			continue
		}
		answer, ok := ask(reader, out, fmt.Sprintf("Registration was removed. Delete log file %s permanently? [y/N] ", printsafe.Line(entry.Path)))
		if ok && answer == "y" {
			if err := target.Delete(); err != nil {
				fmt.Fprintf(out, "Warning: registration was removed, but log file %s was kept: %s.\n", printsafe.Line(entry.Path), printsafe.Line(err.Error()))
			} else {
				fmt.Fprintf(out, "Deleted log file %s.\n", printsafe.Line(entry.Path))
			}
		}
		target.Close()
	}
}

func dockerManagedPath(path string, inventory []docker.Container) bool {
	clean := filepath.Clean(path)
	if clean == "/var/lib/docker" || strings.HasPrefix(clean, "/var/lib/docker/") {
		return true
	}
	parts := strings.Split(clean, "/")
	for i, part := range parts {
		if part == "containers" && i+2 < len(parts) && strings.HasPrefix(parts[i+2], parts[i+1]+"-json.log") {
			return true
		}
	}
	for _, container := range inventory {
		root, _, found := strings.Cut(filepath.Clean(container.LogPath), "/containers/")
		if found && (clean == root || strings.HasPrefix(clean, root+"/")) {
			return true
		}
	}
	return false
}
