package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/takeshiue/jevtri/internal/config"
	"github.com/takeshiue/jevtri/internal/printsafe"
)

type shownConfiguration struct {
	ConfigFile string     `json:"config_file"`
	Minutes    int        `json:"minutes"`
	MaxBytes   int        `json:"max_bytes"`
	SentLog    string     `json:"sent_log"`
	Logs       []shownLog `json:"logs"`
}

type shownLog struct {
	Name            string   `json:"name"`
	Path            string   `json:"path,omitempty"`
	DockerContainer string   `json:"docker_container,omitempty"`
	DockerProject   string   `json:"docker_project,omitempty"`
	JournalUnit     string   `json:"journal_unit,omitempty"`
	TimeFormat      string   `json:"time_format"`
	Timezone        string   `json:"timezone"`
	ReadCompressed  bool     `json:"read_compressed"`
	Groups          []string `json:"groups"`
	MaskRules       int      `json:"mask_rules"`
}

func runShow(options options, env environment) int {
	getenv := env.getenv
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	language, err := helpLanguage(options.lang, getenv)
	if err != nil {
		fmt.Fprintf(env.stderr, "jevtri: %s\n", printsafe.Line(err.Error()))
		return exitUsage
	}
	path, err := filepath.Abs(options.configPath)
	if err != nil {
		fmt.Fprintf(env.stderr, "jevtri: %s\n", printsafe.Line(err.Error()))
		return exitFailure
	}
	cfg, err := config.Load(path)
	if err != nil {
		var invalid *config.Error
		if errors.As(err, &invalid) {
			// Invalid source text may contain secrets even outside supported keys.
			fmt.Fprintf(env.stderr, "jevtri: %s:%d: invalid configuration; check this line\n", printsafe.Line(path), invalid.Line)
		} else {
			fmt.Fprintf(env.stderr, "jevtri: %s\n", printsafe.Line(err.Error()))
		}
		return exitFailure
	}
	shown := shownConfiguration{
		ConfigFile: path, Minutes: cfg.Minutes, MaxBytes: cfg.MaxBytes,
		SentLog: cfg.SentLog, Logs: make([]shownLog, 0, len(cfg.Logs)),
	}
	for _, log := range cfg.Logs {
		// Never expose mask expressions: a literal rule may itself be a secret.
		shown.Logs = append(shown.Logs, shownLog{
			Name: log.Name, Path: log.Path, DockerContainer: log.DockerContainer,
			DockerProject: log.DockerProject, JournalUnit: log.JournalUnit,
			TimeFormat: log.TimeFormat, Timezone: log.Location.String(),
			ReadCompressed: log.ReadCompressed, Groups: append([]string{}, log.Groups...),
			MaskRules: len(log.Masks),
		})
	}
	var output bytes.Buffer
	if options.json {
		encoder := json.NewEncoder(&output)
		encoder.SetIndent("", "  ")
		err = encoder.Encode(shown)
	} else {
		writeShownConfiguration(&output, shown, language)
	}
	if err == nil {
		_, err = io.Copy(env.stdout, &output)
	}
	if err != nil {
		fmt.Fprintf(env.stderr, "jevtri: %s\n", printsafe.Line(err.Error()))
		return exitFailure
	}
	return exitOK
}

func writeShownConfiguration(output io.Writer, shown shownConfiguration, language string) {
	fileLabel, maskLabel := "Configuration file", "Additional mask rules"
	switch language {
	case "ja":
		fileLabel, maskLabel = "設定ファイル", "追加マスクの件数"
	case "zh-CN":
		fileLabel, maskLabel = "配置文件", "附加脱敏规则数量"
	}
	fmt.Fprintf(output, "%s: %s\n\n[general]\nminutes = %d\nmax_bytes = %d\nsent_log = %s\n",
		fileLabel, printsafe.Line(shown.ConfigFile), shown.Minutes, shown.MaxBytes, printsafe.Line(shown.SentLog))
	for _, log := range shown.Logs {
		fmt.Fprintf(output, "\n[log %s]\n", printsafe.Line(log.Name))
		for _, field := range [][2]string{
			{"path", log.Path}, {"docker_container", log.DockerContainer},
			{"docker_project", log.DockerProject}, {"journal_unit", log.JournalUnit},
		} {
			if field[1] != "" {
				fmt.Fprintf(output, "%s = %s\n", field[0], printsafe.Line(field[1]))
			}
		}
		fmt.Fprintf(output, "time_format = %s\ntimezone = %s\nread_compressed = %t\n",
			printsafe.Line(log.TimeFormat), printsafe.Line(log.Timezone), log.ReadCompressed)
		for _, group := range log.Groups {
			fmt.Fprintf(output, "group = %s\n", printsafe.Line(group))
		}
		fmt.Fprintf(output, "# %s: %d\n", maskLabel, log.MaskRules)
	}
}
