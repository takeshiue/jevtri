package setup

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/takeshiue/jevtri/internal/config"
	"github.com/takeshiue/jevtri/internal/printsafe"
)

func chooseRegisteredRemovals(reader *bufio.Reader, out io.Writer, cfg *config.Config, removed []string) []string {
	excluded := make(map[string]bool, len(removed))
	for _, name := range removed {
		excluded[name] = true
	}
	var entries []config.Log
	for _, entry := range cfg.Logs {
		if !excluded[entry.Name] {
			entries = append(entries, entry)
		}
	}
	if len(entries) == 0 {
		return nil
	}
	fmt.Fprintln(out, "Registered logs (removing a registration does not delete its log file):")
	for i, entry := range entries {
		fmt.Fprintf(out, "  %d. [log %s] %s (groups: %s)\n", i+1, printsafe.Line(entry.Name), printsafe.Line(entry.Label()), printsafe.Line(strings.Join(entry.Groups, ",")))
	}
	for {
		answer, ok := ask(reader, out, "Registrations to remove (numbers, ranges, or all; Enter keeps all): ")
		// Deletion must never inherit the registration parser's empty-means-all default.
		if !ok || answer == "" {
			return nil
		}
		indexes, err := ParseSelection(answer, len(entries))
		if err != nil {
			fmt.Fprintf(out, "  %v\n", err)
			continue
		}
		if len(indexes) == 0 {
			return nil
		}
		fmt.Fprintln(out, "Remove these registrations only:")
		var names []string
		for _, i := range indexes {
			names = append(names, entries[i].Name)
			fmt.Fprintf(out, "  [log %s] %s\n", printsafe.Line(entries[i].Name), printsafe.Line(entries[i].Label()))
		}
		confirmed, ok := ask(reader, out, "Remove these registrations? [y/N] ")
		if !ok || confirmed != "y" {
			return nil
		}
		return names
	}
}

func pruneRemovedChanges(removed []string, groups map[string][]string, changes dockerChanges) {
	for _, name := range removed {
		delete(groups, name)
		delete(changes.Paths, name)
		delete(changes.Projects, name)
		delete(changes.Formats, name)
	}
}
