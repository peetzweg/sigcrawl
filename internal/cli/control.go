package cli

import (
	"path/filepath"

	"github.com/openclaw/crawlkit/control"
)

func controlManifest() control.Manifest {
	m := control.NewManifest("sigcrawl", "Signal Crawl", "sigcrawl")
	m.Description = "Local-first Signal Desktop archive crawler."
	m.Branding = control.Branding{
		SymbolName:       "bubble.left.and.bubble.right.fill",
		AccentColor:      "#3a76f0",
		BundleIdentifier: "org.whispersystems.signal-desktop",
	}
	m.Paths = control.Paths{
		DefaultConfig:   filepath.Join(defaultBaseDir(), "backup.json"),
		DefaultDatabase: defaultDBPath(),
		DefaultCache:    filepath.Join(defaultBaseDir(), "cache"),
		DefaultLogs:     filepath.Join(defaultBaseDir(), "logs"),
	}
	m.Capabilities = []string{"metadata", "doctor", "status", "sync", "search", "backup"}
	m.Privacy = control.Privacy{
		ContainsPrivateMessages: true,
		ExportsSecrets:          false,
		LocalOnlyScopes:         []string{"signal-desktop", "sqlite", "encrypted-git-backup"},
	}
	m.Commands = map[string]control.Command{
		"doctor": {Title: "Doctor", Argv: []string{"sigcrawl", "--json", "doctor"}, JSON: true},
		"status": {Title: "Status", Argv: []string{"sigcrawl", "--json", "status"}, JSON: true},
		"sync":   {Title: "Sync", Argv: []string{"sigcrawl", "--json", "sync"}, JSON: true, Mutates: true},
		"search": {Title: "Search", Argv: []string{"sigcrawl", "--json", "search"}, JSON: true},
	}
	return m
}
