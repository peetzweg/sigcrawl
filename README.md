# sigcrawl

Signal Desktop archive CLI. Local-first.

`sigcrawl` reads your Signal Desktop database via [`sigtop`](https://github.com/tbvdm/sigtop), stores a searchable SQLite archive in `~/.sigcrawl/sigcrawl.db`, and can back it up to a private git repo as age-encrypted shards.

Built on [`openclaw/crawlkit`](https://github.com/openclaw/crawlkit) — the Signal sibling of [`telecrawl`](https://github.com/openclaw/telecrawl) (Telegram), [`wacli`](https://github.com/openclaw/wacli) (WhatsApp), [`slacrawl`](https://github.com/openclaw/slacrawl) (Slack), and [`discrawl`](https://github.com/openclaw/discrawl) (Discord). Same CLI vocabulary across all of them.

It is local-first:

- `sync` reads Signal Desktop's SQLCipher database read-only via the sigtop subprocess.
- Normal archive/search commands do not upload data.
- `backup push` uploads only age-encrypted shards when you run it explicitly.
- Message text, conversation names, sender names, and attachment metadata stay inside encrypted backup payloads.

## Install

### Prerequisites

Install [`sigtop`](https://github.com/tbvdm/sigtop) — sigcrawl uses it as the decryption backend for Signal Desktop's SQLCipher database:

```bash
brew install tbvdm/tap/sigtop
```

### sigcrawl

```bash
go install github.com/peetzweg/sigcrawl/cmd/sigcrawl@latest
```

Or build locally:

```bash
git clone https://github.com/peetzweg/sigcrawl
cd sigcrawl
make build
./bin/sigcrawl --help
```

### Docker

```bash
docker build -t sigcrawl .
docker run --rm \
  -v "$HOME/.sigcrawl:/data" \
  -v "$HOME/Library/Application Support/Signal:/signal:ro" \
  sigcrawl --source /signal doctor
```

The image bundles a `sigtop` binary so it is self-contained.

## Sync

```bash
sigcrawl doctor                       # detect Signal Desktop dir + sigtop
sigcrawl sync                         # run full sync (idempotent)
sigcrawl status                       # show archive counts/dates
```

Subsequent runs of `sync` are incremental — only new messages are appended.

## Read

```bash
sigcrawl chats --limit 20
sigcrawl messages --limit 20
sigcrawl messages --chat <conversation-id> --after 2026-01-01
sigcrawl messages --chat <conversation-id> --from-me
sigcrawl messages --media
sigcrawl search "<query>"
sigcrawl search "<query>" --json
```

All commands accept `--json` for machine-readable output.

## Backup (optional)

```bash
sigcrawl backup init --repo ~/Projects/backup-sigcrawl --remote git@github.com:you/backup-sigcrawl.git
sigcrawl backup push
sigcrawl backup status
sigcrawl backup pull   # restore on another machine with the same age identity
```

The repository contains only age-encrypted `.jsonl.gz.age` shards and a `manifest.json` index. Without the identity at `~/.sigcrawl/age.key`, nothing in the repo is readable.

## How it works

1. `sigcrawl sync` invokes `sigtop -d <signal-dir> export-database <tmp>.sqlite`.
2. `sigtop` resolves Signal Desktop's safeStorage key via the OS keystore (macOS Keychain / Windows DPAPI / Linux libsecret-kwallet-gnome-keyring), decrypts the SQLCipher DB, writes a plaintext SQLite copy.
3. `sigcrawl` opens the plaintext copy with pure-Go SQLite (`modernc.org/sqlite`), walks the `conversations` / `messages` tables, parses the rich `messages.json` blob (attachments, reactions, mentions, quotes, edits), and upserts into the archive at `~/.sigcrawl/sigcrawl.db` (with FTS5).
4. The temp plaintext copy is deleted.

`sigcrawl` never touches the SQLCipher key directly — that responsibility stays with `sigtop`, which actively tracks Signal Desktop's encryption/keystore changes.

## Not supported (v0)

- iOS Signal (no file-level export exists).
- Android `.backup` files — use [`signalbackup-tools`](https://github.com/bepaald/signalbackup-tools) directly.
- Live tail via `signal-cli` — planned for v2.

## License

MIT
