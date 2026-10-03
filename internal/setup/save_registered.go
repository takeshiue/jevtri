package setup

import (
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/takeshiue/jevtri/internal/config"
	"github.com/takeshiue/jevtri/internal/docker"
)

type configBlock struct {
	Name  string
	Lines []string
}

const configurationSample = "# Configuration syntax example (inactive):\n# [log example]\n# path = /path/to/app.log # Optional comment\n# time_format = iso-space\n# group = app\n# docker_container = container-name # Optional Docker metadata\n# docker_project = project-name # Optional Compose metadata\n\n"

func withConfigurationSample(content string) string {
	if strings.Contains(content, "# Configuration syntax example (inactive):") {
		return content
	}
	return configurationSample + content
}

func ensureRegisteredSample(path string, out io.Writer) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	content := withConfigurationSample(string(data))
	if content == string(data) {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if _, err := config.Parse(strings.NewReader(content), path); err != nil {
		return err
	}
	if err := replaceFile(path, content, info.Mode().Perm()); err != nil {
		return err
	}
	fmt.Fprintf(out, "Added an inactive configuration syntax example to %s.\n", path)
	return nil
}

func splitConfigBlocks(content string) []configBlock {
	blocks := []configBlock{{}}
	for _, line := range strings.SplitAfter(content, "\n") {
		content, _ := config.StripComment(line)
		trimmed := strings.TrimSpace(content)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			name := ""
			fields := strings.Fields(trimmed[1 : len(trimmed)-1])
			if len(fields) == 2 && fields[0] == "log" {
				name = fields[1]
			}
			blocks = append(blocks, configBlock{Name: name})
		}
		last := len(blocks) - 1
		blocks[last].Lines = append(blocks[last].Lines, line)
	}
	return blocks
}

func setPath(lines []string, path string) []string {
	result := append([]string(nil), lines...)
	found := false
	for index, line := range result {
		content, _, _ := config.SplitComment(line)
		key, _, ok := strings.Cut(strings.TrimSpace(content), "=")
		if ok && strings.TrimSpace(key) == "path" {
			result[index] = updatedConfigLine(line, "path = "+formattedValue(path))
			found = true
		}
	}
	if !found && len(result) > 0 {
		result = append(result[:1], append([]string{"path = " + formattedValue(path) + "\n"}, result[1:]...)...)
	}
	return result
}

func ensureLine(lines []string, key, value string) []string {
	last := -1
	lastValue := ""
	for index, line := range lines {
		content, _ := config.StripComment(line)
		candidate, present, ok := strings.Cut(strings.TrimSpace(content), "=")
		if ok && strings.TrimSpace(candidate) == key {
			last = index
			lastValue = strings.TrimSpace(present)
			if lastValue == "\"\"" || lastValue == "''" {
				lastValue = ""
			}
		}
	}
	if last >= 0 {
		if key == "time_format" && lastValue == "" {
			lines = append([]string(nil), lines...)
			lines[last] = updatedConfigLine(lines[last], key+" = "+formattedValue(value))
		}
		return lines
	}
	if len(lines) > 0 && lines[len(lines)-1] != "" && !strings.HasSuffix(lines[len(lines)-1], "\n") {
		lines[len(lines)-1] += "\n"
	}
	return append(lines, key+" = "+formattedValue(value)+"\n")
}

func formattedValue(value string) string {
	formatted, err := config.FormatValue(value)
	if err != nil {
		return "\""
	}
	return formatted
}

func updatedConfigLine(original, replacement string) string {
	_, comment, _ := config.SplitComment(original)
	if comment != "" {
		return replacement + comment
	}
	if strings.HasSuffix(original, "\n") {
		replacement += "\n"
	}
	return replacement
}

func validateSerializedCandidates(parsed *config.Config, candidates []Candidate) error {
	byName := map[string]config.Log{}
	for _, entry := range parsed.Logs {
		byName[entry.Name] = entry
	}
	for _, candidate := range candidates {
		entry, ok := byName[candidate.Name]
		format := candidate.TimeFormat
		if format == "" && (candidate.DockerContainer != "" || candidate.DockerProject != "") {
			format = "docker-json"
		}
		groups := candidate.Groups
		if len(groups) == 0 && candidate.Group != "" {
			groups = []string{candidate.Group}
		}
		if !ok || entry.Path != candidate.Path || entry.TimeFormat != format || entry.DockerContainer != candidate.DockerContainer || entry.DockerProject != candidate.DockerProject || entry.JournalUnit != candidate.JournalUnit || !slices.Equal(entry.Groups, groups) {
			return fmt.Errorf("generated configuration differs from the selected log; configuration was not changed")
		}
	}
	return nil
}

func saveRegisteredUpdate(path string, removed []string, groups map[string][]string, candidates []Candidate, changes dockerChanges, root string) error {
	for _, values := range []map[string]string{changes.Paths, changes.Formats} {
		for _, value := range values {
			if _, err := config.FormatValue(value); err != nil {
				return err
			}
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	drop := map[string]bool{}
	for _, name := range removed {
		drop[name] = true
	}
	var builder strings.Builder
	for _, block := range splitConfigBlocks(string(data)) {
		if block.Name != "" && drop[block.Name] {
			continue
		}
		if members, ok := changes.Projects[block.Name]; ok {
			for _, member := range members {
				lines := setPath(block.Lines, member.Path)
				lines[0] = updatedConfigLine(lines[0], "[log "+member.Name+"]")
				lines = ensureLine(lines, "docker_container", member.DockerContainer)
				lines = ensureLine(lines, "time_format", member.TimeFormat)
				if len(member.Groups) > 0 {
					lines = ensureLine(lines, "group", member.Groups[0])
				}
				builder.WriteString(strings.Join(lines, ""))
			}
			continue
		}
		lines := block.Lines
		if changed, ok := changes.Paths[block.Name]; ok {
			lines = setPath(lines, changed)
		}
		if format, ok := changes.Formats[block.Name]; ok {
			lines = ensureLine(lines, "time_format", format)
		}
		if assigned, ok := groups[block.Name]; ok {
			lines = append([]string(nil), lines...)
			if len(lines) > 0 && lines[len(lines)-1] != "" && !strings.HasSuffix(lines[len(lines)-1], "\n") {
				lines[len(lines)-1] += "\n"
			}
			at := len(lines)
			for at > 0 && strings.TrimSpace(lines[at-1]) == "" {
				at--
			}
			var inserted []string
			for _, group := range assigned {
				inserted = append(inserted, "group = "+formattedValue(group)+"\n")
			}
			lines = append(lines[:at], append(inserted, lines[at:]...)...)
		}
		builder.WriteString(strings.Join(lines, ""))
	}
	content := withConfigurationSample(builder.String())
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += renderLogs(candidates)
	parsed, err := config.Parse(strings.NewReader(content), path)
	if err != nil {
		return err
	}
	if err := validateSerializedCandidates(parsed, candidates); err != nil {
		return err
	}
	for _, members := range changes.Projects {
		if err := validateSerializedCandidates(parsed, members); err != nil {
			return err
		}
	}
	for _, entry := range parsed.Logs {
		if expected, ok := changes.Paths[entry.Name]; ok && entry.Path != expected {
			return fmt.Errorf("generated path differs from the approved path; configuration was not changed")
		}
		if expected, ok := changes.Formats[entry.Name]; ok && entry.TimeFormat != expected {
			return fmt.Errorf("generated time_format differs from the approved value; configuration was not changed")
		}
		if expected, ok := groups[entry.Name]; ok && !slices.Equal(entry.Groups, expected) {
			return fmt.Errorf("generated groups differ from the approved groups; configuration was not changed")
		}
	}
	seen := map[string]bool{}
	for _, entry := range parsed.Logs {
		if entry.Path != "" {
			if seen[entry.Path] {
				return fmt.Errorf("duplicate registered path; configuration was not changed")
			}
			seen[entry.Path] = true
		}
	}
	if err := validateCandidates(candidates, root); err != nil {
		return err
	}
	for _, changed := range changes.Paths {
		if err := docker.ValidateLogPath(root, changed); err != nil {
			return err
		}
	}
	for _, entry := range parsed.Logs {
		if _, changing := changes.Formats[entry.Name]; changing {
			if err := docker.ValidateLogPath(root, entry.Path); err != nil {
				return err
			}
		}
	}
	for _, members := range changes.Projects {
		if err := validateCandidates(members, root); err != nil {
			return err
		}
	}
	return replaceFile(path, content, info.Mode().Perm())
}
