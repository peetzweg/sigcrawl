package signaldesktop

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

type Options struct {
	Path   string
	Sigtop string
}

type Report struct {
	Path           string `json:"path"`
	Exists         bool   `json:"exists"`
	Accessible     bool   `json:"accessible"`
	Store          string `json:"store"`
	ConfigJSON     bool   `json:"config_json"`
	DBSqlite       bool   `json:"db_sqlite"`
	AttachmentsDir bool   `json:"attachments_dir"`
	SigtopBinary   string `json:"sigtop_binary,omitempty"`
	SigtopVersion  string `json:"sigtop_version,omitempty"`
	Note           string `json:"note,omitempty"`
	Error          string `json:"error,omitempty"`
}

func DefaultPath() string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Signal")
	case "windows":
		if appData := strings.TrimSpace(os.Getenv("APPDATA")); appData != "" {
			return filepath.Join(appData, "Signal")
		}
		return filepath.Join(home, "AppData", "Roaming", "Signal")
	default:
		if cfgHome := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); cfgHome != "" {
			return filepath.Join(cfgHome, "Signal")
		}
		return filepath.Join(home, ".config", "Signal")
	}
}

func ResolveSigtop(explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		if _, err := os.Stat(explicit); err == nil {
			return explicit, nil
		}
	}
	return exec.LookPath("sigtop")
}

func Probe(ctx context.Context, opts Options) Report {
	path := strings.TrimSpace(opts.Path)
	if path == "" {
		path = DefaultPath()
	}
	report := Report{Path: path, Store: "missing"}
	info, err := os.Stat(path)
	if err != nil {
		report.Error = err.Error()
	} else {
		report.Exists = true
		if !info.IsDir() {
			report.Store = "unsupported-file"
			report.Error = "path is not a directory"
			return report
		}
	}
	if _, err := os.Stat(filepath.Join(path, "config.json")); err == nil {
		report.ConfigJSON = true
	}
	if _, err := os.Stat(filepath.Join(path, "sql", "db.sqlite")); err == nil {
		report.DBSqlite = true
	}
	if info, err := os.Stat(filepath.Join(path, "attachments.noindex")); err == nil && info.IsDir() {
		report.AttachmentsDir = true
	}
	report.Accessible = report.ConfigJSON && report.DBSqlite
	switch {
	case report.Accessible:
		report.Store = "signal-desktop-sqlcipher"
	case report.Exists:
		report.Store = "incomplete"
		report.Note = "Signal Desktop directory found, but config.json or sql/db.sqlite is missing. Open the Signal Desktop app once to initialize."
	}
	if binPath, err := ResolveSigtop(opts.Sigtop); err == nil {
		report.SigtopBinary = binPath
		if out, err := exec.CommandContext(ctx, binPath, "-v").CombinedOutput(); err == nil {
			report.SigtopVersion = strings.TrimSpace(string(out))
		}
	} else {
		if report.Note != "" {
			report.Note += " "
		}
		report.Note += "Install sigtop to enable sync: `brew install tbvdm/tap/sigtop`."
	}
	return report
}
