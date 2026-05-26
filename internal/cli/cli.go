package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/peetzweg/sigcrawl/internal/backup"
	"github.com/peetzweg/sigcrawl/internal/signaldesktop"
	"github.com/peetzweg/sigcrawl/internal/store"
)

type cliError struct {
	code int
	err  error
}

func (e *cliError) Error() string { return e.err.Error() }
func (e *cliError) Unwrap() error { return e.err }

func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, context.Canceled) {
		return 1
	}
	var codeErr *cliError
	if errors.As(err, &codeErr) {
		return codeErr.code
	}
	return 1
}

type runtime struct {
	ctx    context.Context
	stdout io.Writer
	stderr io.Writer
	json   bool
	dbPath string
	source string
	sigtop string
}

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printUsage(stdout)
		return nil
	}
	global := flag.NewFlagSet("sigcrawl", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	jsonOut := global.Bool("json", false, "")
	dbPath := global.String("db", defaultDBPath(), "")
	source := global.String("source", "", "")
	sigtop := global.String("sigtop", "", "")
	versionFlag := global.Bool("version", false, "")
	if err := global.Parse(args); err != nil {
		return usageErr(err)
	}
	if *versionFlag {
		_, _ = io.WriteString(stdout, version+"\n")
		return nil
	}
	rest := global.Args()
	if len(rest) == 0 || rest[0] == "help" || rest[0] == "--help" || rest[0] == "-h" {
		printUsage(stdout)
		return nil
	}
	if rest[0] == "version" {
		_, _ = io.WriteString(stdout, version+"\n")
		return nil
	}
	r := &runtime{
		ctx:    ctx,
		stdout: stdout,
		stderr: stderr,
		json:   *jsonOut,
		dbPath: *dbPath,
		source: *source,
		sigtop: *sigtop,
	}
	return r.dispatch(rest)
}

func (r *runtime) dispatch(args []string) error {
	switch args[0] {
	case "metadata":
		return r.print(controlManifest())
	case "doctor":
		return r.runDoctor(args[1:])
	case "sync", "import":
		return r.runSync(args[1:])
	case "status":
		return r.runStatus(args[1:])
	case "chats":
		return r.runChats(args[1:])
	case "messages":
		return r.runMessages(args[1:])
	case "search":
		return r.runSearch(args[1:])
	case "backup":
		return r.runBackup(args[1:])
	default:
		return usageErr(fmt.Errorf("unknown command %q", args[0]))
	}
}

func (r *runtime) withStore(fn func(*store.Store) error) error {
	st, err := store.Open(r.ctx, r.dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	return fn(st)
}

func (r *runtime) runDoctor(args []string) error {
	fs := flag.NewFlagSet("sigcrawl doctor", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("path", r.source, "")
	sigtop := fs.String("sigtop", r.sigtop, "")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	report := signaldesktop.Probe(r.ctx, signaldesktop.Options{Path: *path, Sigtop: *sigtop})
	return r.printProbe(report)
}

func (r *runtime) runSync(args []string) error {
	fs := flag.NewFlagSet("sigcrawl sync", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("path", r.source, "")
	sigtop := fs.String("sigtop", r.sigtop, "")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	return r.withStore(func(st *store.Store) error {
		result, err := signaldesktop.Import(r.ctx, signaldesktop.ImportOptions{Path: *path, Sigtop: *sigtop}, st.Path())
		if err != nil {
			return err
		}
		if err := st.Upsert(r.ctx, result.Stats, result.Chats, result.Messages); err != nil {
			return err
		}
		return r.print(result.Stats)
	})
}

func (r *runtime) runStatus(args []string) error {
	fs := flag.NewFlagSet("sigcrawl status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	return r.withStore(func(st *store.Store) error {
		status, err := st.Status(r.ctx)
		if err != nil {
			return err
		}
		return r.print(status)
	})
}

func (r *runtime) runChats(args []string) error {
	fs := flag.NewFlagSet("sigcrawl chats", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	limit := fs.Int("limit", 50, "")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	return r.withStore(func(st *store.Store) error {
		chats, err := st.ListChats(r.ctx, *limit)
		if err != nil {
			return err
		}
		return r.print(chats)
	})
}

func (r *runtime) runMessages(args []string) error {
	filter, err := r.messageFilter("sigcrawl messages", args, false)
	if err != nil {
		return err
	}
	return r.withStore(func(st *store.Store) error {
		messages, err := st.Messages(r.ctx, filter)
		if err != nil {
			return err
		}
		return r.print(messages)
	})
}

func (r *runtime) runSearch(args []string) error {
	filter, err := r.messageFilter("sigcrawl search", args, true)
	if err != nil {
		return err
	}
	return r.withStore(func(st *store.Store) error {
		messages, err := st.Search(r.ctx, filter)
		if err != nil {
			return err
		}
		return r.print(messages)
	})
}

func (r *runtime) messageFilter(name string, args []string, requireQuery bool) (store.MessageFilter, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var filter store.MessageFilter
	fs.StringVar(&filter.ChatID, "chat", "", "")
	fs.StringVar(&filter.Sender, "sender", "", "")
	fs.IntVar(&filter.Limit, "limit", 50, "")
	after := fs.String("after", "", "")
	before := fs.String("before", "", "")
	fromMe := fs.Bool("from-me", false, "")
	fromThem := fs.Bool("from-them", false, "")
	fs.BoolVar(&filter.HasMedia, "media", false, "")
	fs.BoolVar(&filter.Asc, "asc", false, "")
	if err := fs.Parse(args); err != nil {
		return filter, usageErr(err)
	}
	if requireQuery {
		if fs.NArg() != 1 {
			return filter, usageErr(errors.New("search takes exactly one query"))
		}
		filter.Query = fs.Arg(0)
	} else if fs.NArg() != 0 {
		return filter, usageErr(errors.New("messages takes flags only"))
	}
	if *after != "" {
		t, err := parseDate(*after)
		if err != nil {
			return filter, usageErr(err)
		}
		filter.After = &t
	}
	if *before != "" {
		t, err := parseDate(*before)
		if err != nil {
			return filter, usageErr(err)
		}
		filter.Before = &t
	}
	if *fromMe && *fromThem {
		return filter, usageErr(errors.New("--from-me and --from-them conflict"))
	}
	if *fromMe || *fromThem {
		v := *fromMe
		filter.FromMe = &v
	}
	return filter, nil
}

func (r *runtime) runBackup(args []string) error {
	if len(args) == 0 {
		return usageErr(errors.New("backup needs subcommand: init, push, pull, status"))
	}
	switch args[0] {
	case "init":
		return r.backupInit(args[1:])
	case "push":
		return r.backupPush(args[1:])
	case "pull":
		return r.backupPull(args[1:])
	case "status":
		return r.backupStatus(args[1:])
	default:
		return usageErr(fmt.Errorf("unknown backup command %q", args[0]))
	}
}

func backupFlags(name string) (*flag.FlagSet, *backup.Options, *bool) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	opts := &backup.Options{}
	fs.StringVar(&opts.ConfigPath, "config", backup.DefaultConfigPath(), "")
	fs.StringVar(&opts.Repo, "repo", "", "")
	fs.StringVar(&opts.Remote, "remote", "", "")
	fs.StringVar(&opts.Identity, "identity", "", "")
	fs.Func("recipient", "", func(value string) error {
		opts.Recipients = append(opts.Recipients, value)
		return nil
	})
	noPush := fs.Bool("no-push", false, "")
	return fs, opts, noPush
}

func (r *runtime) backupInit(args []string) error {
	fs, opts, noPush := backupFlags("sigcrawl backup init")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	opts.Push = !*noPush
	cfg, recipient, err := backup.Init(r.ctx, *opts)
	if err != nil {
		return err
	}
	return r.print(map[string]any{"repo": cfg.Repo, "remote": cfg.Remote, "identity": cfg.Identity, "recipient": recipient})
}

func (r *runtime) backupPush(args []string) error {
	fs, opts, noPush := backupFlags("sigcrawl backup push")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	opts.Push = !*noPush
	return r.withStore(func(st *store.Store) error {
		result, err := backup.Push(r.ctx, st, *opts)
		if err != nil {
			return err
		}
		return r.print(result)
	})
}

func (r *runtime) backupPull(args []string) error {
	fs, opts, _ := backupFlags("sigcrawl backup pull")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	return r.withStore(func(st *store.Store) error {
		result, err := backup.Pull(r.ctx, st, *opts)
		if err != nil {
			return err
		}
		return r.print(result)
	})
}

func (r *runtime) backupStatus(args []string) error {
	fs, opts, _ := backupFlags("sigcrawl backup status")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	manifest, repo, err := backup.Status(r.ctx, *opts)
	if err != nil {
		return err
	}
	return r.print(map[string]any{"repo": repo, "manifest": manifest})
}

func (r *runtime) printProbe(report signaldesktop.Report) error {
	if r.json {
		enc := json.NewEncoder(r.stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	fmt.Fprintf(r.stdout, "path: %s\n", report.Path)
	fmt.Fprintf(r.stdout, "exists: %t\n", report.Exists)
	fmt.Fprintf(r.stdout, "accessible: %t\n", report.Accessible)
	fmt.Fprintf(r.stdout, "store: %s\n", report.Store)
	fmt.Fprintf(r.stdout, "config_json: %t\n", report.ConfigJSON)
	fmt.Fprintf(r.stdout, "db_sqlite: %t\n", report.DBSqlite)
	fmt.Fprintf(r.stdout, "attachments_dir: %t\n", report.AttachmentsDir)
	if report.SigtopBinary != "" {
		fmt.Fprintf(r.stdout, "sigtop_binary: %s\n", report.SigtopBinary)
	} else {
		fmt.Fprintf(r.stdout, "sigtop_binary: (not found)\n")
	}
	if report.SigtopVersion != "" {
		fmt.Fprintf(r.stdout, "sigtop_version: %s\n", report.SigtopVersion)
	}
	if report.Note != "" {
		fmt.Fprintf(r.stdout, "note: %s\n", report.Note)
	}
	if report.Error != "" {
		fmt.Fprintf(r.stdout, "error: %s\n", report.Error)
	}
	return nil
}

func (r *runtime) print(v any) error {
	enc := json.NewEncoder(r.stdout)
	if r.json {
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	switch value := v.(type) {
	case store.Status:
		fmt.Fprintf(r.stdout, "db_path: %s\nchats: %d\nmessages: %d\nmedia_messages: %d\n",
			value.DBPath, value.Chats, value.Messages, value.MediaMessages)
		if !value.OldestMessage.IsZero() {
			fmt.Fprintf(r.stdout, "oldest_message: %s\n", value.OldestMessage.Format(time.RFC3339))
		}
		if !value.NewestMessage.IsZero() {
			fmt.Fprintf(r.stdout, "newest_message: %s\n", value.NewestMessage.Format(time.RFC3339))
		}
		if !value.LastImportAt.IsZero() {
			fmt.Fprintf(r.stdout, "last_import_at: %s\n", value.LastImportAt.Format(time.RFC3339))
		}
		if value.LastSource != "" {
			fmt.Fprintf(r.stdout, "last_source: %s\n", value.LastSource)
		}
		return nil
	case store.ImportStats:
		fmt.Fprintf(r.stdout, "source_path: %s\ndb_path: %s\nchats: %d\nmessages: %d\nmedia_messages: %d\nstarted_at: %s\nfinished_at: %s\n",
			value.SourcePath, value.DBPath, value.Chats, value.Messages, value.MediaMessages,
			value.StartedAt.Format(time.RFC3339), value.FinishedAt.Format(time.RFC3339))
		return nil
	default:
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
}

func usageErr(err error) error {
	return &cliError{code: 2, err: err}
}

func printUsage(w io.Writer) {
	_, _ = io.WriteString(w, `sigcrawl: Signal Desktop archive crawler

usage:
  sigcrawl [--json] doctor [--path PATH] [--sigtop PATH]
  sigcrawl [--json] sync [--path PATH] [--sigtop PATH]
  sigcrawl [--json] status
  sigcrawl [--json] chats [--limit N]
  sigcrawl [--json] messages [--chat ID] [--sender ID] [--after DATE] [--before DATE] [--limit N] [--media] [--from-me|--from-them] [--asc]
  sigcrawl [--json] search "query" [--chat ID] [--limit N]
  sigcrawl [--json] backup init|push|pull|status
  sigcrawl [--json] metadata
  sigcrawl version

setup:
  Install sigtop (used as the decryption backend):
    brew install tbvdm/tap/sigtop

notes:
  sync reads Signal Desktop's encrypted SQLCipher database read-only via sigtop,
  normalizes messages into ~/.sigcrawl/sigcrawl.db with FTS5, and is incremental.
  backup writes age-encrypted shards to a git repo (no plaintext leaves the host).
`)
}

func defaultDBPath() string {
	return filepath.Join(defaultBaseDir(), "sigcrawl.db")
}

func defaultBaseDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".sigcrawl"
	}
	return filepath.Join(home, ".sigcrawl")
}

func parseDate(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02", value); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("invalid date %q", value)
}
