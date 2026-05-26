# matcrawl — handoff brief for a fresh-context agent

> **You are starting a new project.** This document is everything you need to build `matcrawl`, a Matrix protocol archive CLI in the openclaw `*crawl` family, modelled on the design that just shipped successfully for [`peetzweg/sigcrawl`](https://github.com/peetzweg/sigcrawl). It is self-contained — you don't need to read the rest of the sigcrawl source unless you want to, but you should look at it as a reference implementation (this file lives in the sigcrawl repo's root for that reason).
>
> **Owner:** `@peetzweg` (Philip Poloczek). New repo is expected to live at `github.com/peetzweg/matcrawl` to start (transfer to `openclaw/matcrawl` later, same path wacrawl took to become openclaw/wacli).

---

## 1. Mission

Build a single Go CLI binary that:

1. Logs into a user's Matrix account (homeserver of their choice — matrix.org, self-hosted Synapse, Dendrite, etc.).
2. Decrypts End-to-End Encrypted rooms using the user's Megolm keys.
3. Pulls full message history per room.
4. Stores it in a local SQLite archive at `~/.matcrawl/matcrawl.db` with FTS5 full-text search.
5. Exposes the same CLI surface as the rest of the family: `doctor / login / sync / status / rooms / messages / search / backup`.
6. Optionally pushes an age-encrypted, sharded backup of the archive to a private git repo.

End-user install path is `brew install peetzweg/tap/matcrawl` after the first release ships.

---

## 2. The openclaw `*crawl` family — shared shape

Every tool in this family follows the same contract. Treat this as a hard constraint, not a guideline — the whole point is that an LLM/agent can talk to telecrawl, wacli, slacrawl, sigcrawl, matcrawl with the same vocabulary.

| Aspect | Convention |
|---|---|
| Language | Go (pure-Go, `CGO_ENABLED=0`) |
| SQLite driver | `modernc.org/sqlite` (pure-Go, no libsqlite needed) |
| Archive location | `~/.<tool>/<tool>.db` (e.g. `~/.matcrawl/matcrawl.db`) |
| FTS | SQLite FTS5 virtual table on message bodies |
| CLI subcommands | `doctor` `sync` `status` `chats` (or `rooms`) `messages` `search` `backup push|pull|init|status` `metadata` `version` |
| `--json` flag | Global; switches all output to JSON for agent consumption |
| Metadata manifest | `<tool> metadata` returns a `control.Manifest` JSON describing the tool (used by [`crawlkit`](https://github.com/openclaw/crawlkit)) |
| Backup format | age-encrypted `.jsonl.gz.age` shards in a git repo, sharded `messages/<year>/<month>.jsonl.gz.age`, with a top-level `manifest.json` |
| Release tooling | release-please + GoReleaser (see §10) |
| Distribution | Homebrew tap (`peetzweg/homebrew-tap`) + `go install` + GitHub Releases |

The shared infrastructure module is `github.com/openclaw/crawlkit` (control manifest types, age helpers, etc.). For v0 you only need `crawlkit/control` and `crawlkit/backup` — both are stable and well-isolated.

---

## 3. Why matcrawl is fundamentally different from sigcrawl/wacli/telecrawl

The family's other members all read **local storage of a closed desktop client** because the platforms either don't expose full message history via API (Signal), use opaque pairing flows (WhatsApp), or require heavyweight SDK setups (Telegram MTProto).

**Matrix is the opposite: it's an open protocol with a documented Client-Server API that supports full history backfill natively.**

So the sigcrawl pattern — "shell out to sigtop, read the SQLCipher dump" — has **no analog here, and you should not try to build one**.

Specifically: **do NOT try to read Element Desktop's IndexedDB.** Confirmed during research (research summary at the bottom of this doc). The reasons:

- Element Desktop's data sits in **IndexedDB (LevelDB-backed)** under `~/Library/Application Support/Element/IndexedDB/` (macOS) / `~/.config/Element/IndexedDB/` (Linux) / `%APPDATA%\Element\IndexedDB\` (Windows) — not SQL.
- The schema is **matrix-js-sdk's internal one, undocumented and changes between releases.**
- Only some content is encrypted at rest. E2EE room content is decrypted in-memory on demand from Megolm sessions also held in IndexedDB.
- Element bundles a separate Rust component called [Seshat](https://github.com/matrix-org/seshat) that maintains a SQLCipher-encrypted FTS index, but it's a search index, not the message store.
- Net: writing a `sigtop` equivalent for Element is a multi-month reverse-engineering project with break-on-every-release maintenance.

**Instead: speak the Matrix Client-Server API directly.** That's the right path and matcrawl should commit to it from day one.

There's also a separate Matrix tool called **[pantalaimon](https://github.com/matrix-org/pantalaimon)** that used to be a "decrypting Matrix proxy" people built on top of. **It was archived on 2026-04-08.** Don't depend on it.

---

## 4. Recommended architecture

### 4.1 Library: mautrix-go

Use **[`maunium.net/go/mautrix`](https://github.com/mautrix/go)** (also known as mautrix-go). It is:

- Maintained by Tulir Asokan (creator of the mautrix bridges) — actively released; last big release Aug 2025 covered room version 12.
- The same SDK that powers [gomuks](https://github.com/gomuks/gomuks) (full TUI Matrix client) and every mautrix bridge.
- Pure-Go-buildable: use the `goolm` build tag (`go build -tags goolm`) to get a pure-Go Olm implementation, so the binary has no libolm/CGO dependency. **This matches the rest of the `*crawl` family's pure-Go discipline.**
- Has a real E2EE story: `mautrix/crypto` contains `OlmMachine` with Megolm encrypt/decrypt, server-side key backup (`CreateKeyBackupVersion` / `GetKeyBackup`), cross-signing, and SAS verification. `mautrix/crypto/cryptohelper` wires it into `Client` with a SQLite-backed `CryptoStore` and a "pickle key" (a passphrase that encrypts the local crypto store).

Don't pick alternatives:

- `matrix-rust-sdk` — official Element SDK, but Rust, not Go. Would break the family's pure-Go discipline.
- `matrix-nio` — Python. Same problem, plus Python venv distribution issues.
- Writing the Client-Server API directly — possible but you'd be reimplementing Olm/Megolm crypto and that's a security cliff. Don't.

### 4.2 Login model

Three login mechanisms exist in Matrix. For v0:

**Recommended v0 login: homeserver URL + access token + device ID.**

Reasoning:
- Trivial to obtain — Element → Settings → Help & About → Advanced → "Access Token". User pastes three strings.
- Works regardless of whether the homeserver uses password auth, SSO, or OIDC (most do, including matrix.org).
- Most important: by using an **existing verified device's** identity (the one currently logged in via Element), the crypto state you import from the manual key export (§4.3) will map cleanly to a known device on your account.

v1 additions (not v0):
- `m.login.password` for self-hosted Synapse users without SSO. Easy to add.
- `m.login.sso` with localhost callback (open browser, listen on `http://localhost:<port>/callback`).
- Interactive QR-code or SAS device verification (MSC3906 / MSC4108) — eliminates the manual key export step entirely. This is the polish path.

### 4.3 E2EE key acquisition — the real challenge

Matrix end-to-end encryption is Megolm-per-room. To decrypt a room's history, matcrawl needs the **Megolm sessions** for every key rotation that ever happened in that room. Three ways to get them:

| Path | Friction | When to use |
|---|---|---|
| **Manual `.txt` export from Element** (Settings → Security & Privacy → Export E2E room keys → passphrase → `keys.txt`) | One settings click + passphrase | **v0** |
| **Server-side key backup via 4S/SSSS recovery key** (`/room_keys` endpoint, decrypted with the user's 4-element Secret Storage key) | Asking for a 4S recovery key is heavier UX | v1 |
| **Cross-signing + key forwarding** (matcrawl registers as a new device, user verifies it once in Element, then keys forward automatically) | Cleanest long-term — but requires implementing SAS or QR verification in the CLI | v1 |

For v0: implement the manual `.txt` export path. The file format is documented at https://spec.matrix.org/latest/client-server-api/#key-exports — it's an `-----BEGIN/END MEGOLM SESSION DATA-----` armored bundle around AES-CTR-encrypted JSON. Decrypt with the user-supplied passphrase, feed each session into mautrix's `CryptoStore` via the `OlmMachine`'s `ImportKeys` API (or equivalent — check the cryptohelper docs for the exact entry point at implementation time).

This is what [`russelldavies/matrix-archive`](https://github.com/russelldavies/matrix-archive) (Python) does and it works in practice. Copy that UX shape.

**Documented v0 limitation in the README:** matcrawl can only decrypt history that was active at key-export time. If the user joins a new encrypted room *after* exporting keys, they must re-export. v1 (cross-signing + key forwarding) lifts this restriction.

### 4.4 Sync + backfill strategy

1. **First-time bootstrap**: `/sync` once with a long timeout to learn the room list and current `next_batch` token. Persist `next_batch` and per-room `prev_batch` tokens in a `sync_state` table.
2. **Per-room historical backfill**: for each joined room, paginate `/rooms/{id}/messages?dir=b&from=<prev_batch>` until the response's `end` token equals `start` (i.e., room exhausted). Decrypt each `m.room.encrypted` event through the `OlmMachine`. Insert into the local `messages` table.
3. **Incremental sync**: subsequent `sync` invocations resume from the persisted `next_batch` token, fetch new events forward, decrypt, insert.
4. **Resumable**: every paginated `/messages` call must checkpoint its `prev_batch` token so a crash or `Ctrl-C` doesn't restart from scratch.

### 4.5 Homeserver rate limits — handle them

- **`/messages` is slow on matrix.org/Synapse** ([known issue #13356](https://github.com/matrix-org/synapse/issues/13356) — 80–120s per call has been reported under load). Use a generous client timeout (~5 minutes per call).
- **`M_LIMIT_EXCEEDED`** responses come with a `retry_after_ms` field. Honor it. Sleep, then retry. Don't do exponential backoff with no cap — Matrix gives you the exact wait time.
- **`/sync`** is effectively unrate-limited. No special handling.

---

## 5. Schema design

Mirror sigcrawl's schema where concepts overlap; specialize where Matrix has unique structure. Matrix has different terminology, so the table names follow Matrix's:

```sql
CREATE TABLE rooms (
  id TEXT PRIMARY KEY,             -- '!room_id:server.example'
  canonical_alias TEXT,             -- '#room-name:server.example'
  name TEXT,                        -- m.room.name event
  topic TEXT,                       -- m.room.topic event
  avatar_mxc TEXT,                  -- mxc://… avatar URI
  is_direct INTEGER NOT NULL DEFAULT 0,
  is_encrypted INTEGER NOT NULL DEFAULT 0,
  encryption_algorithm TEXT,        -- 'm.megolm.v1.aes-sha2' etc.
  member_count INTEGER NOT NULL DEFAULT 0,
  joined_at INTEGER,                -- unix seconds of m.room.member join
  last_event_ts INTEGER,
  message_count INTEGER NOT NULL DEFAULT 0,
  prev_batch TEXT                   -- pagination token; for resumable backfill
);

CREATE TABLE room_members (
  room_id TEXT NOT NULL,
  user_id TEXT NOT NULL,            -- '@user:server.example'
  display_name TEXT,
  avatar_mxc TEXT,
  membership TEXT NOT NULL,         -- 'join' | 'leave' | 'invite' | 'ban'
  power_level INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (room_id, user_id)
);

CREATE TABLE messages (
  rowid INTEGER PRIMARY KEY AUTOINCREMENT,
  room_id TEXT NOT NULL,
  event_id TEXT NOT NULL,           -- Matrix event ID, '$abc…'
  sender TEXT NOT NULL,             -- '@user:server.example'
  sender_display_name TEXT,         -- denormalized for output convenience
  origin_server_ts INTEGER NOT NULL,
  msgtype TEXT NOT NULL,            -- 'm.text' | 'm.image' | 'm.notice' | …
  body TEXT,                        -- decrypted plaintext
  formatted_body TEXT,              -- HTML, if present
  was_encrypted INTEGER NOT NULL DEFAULT 0,  -- was the wire event m.room.encrypted?
  decrypt_status TEXT,              -- 'ok' | 'missing_keys' | 'failed' | NULL for non-E2EE
  decrypt_error TEXT,               -- e.g. 'megolm session not found'
  attachments_json TEXT,            -- mxc URIs, content types, thumbs
  edits_json TEXT,                  -- m.replace relations
  reactions_json TEXT,              -- m.annotation relations
  thread_id TEXT,                   -- m.thread parent event_id, if any
  reply_to_event_id TEXT,
  raw_json TEXT,                    -- canonical event JSON for round-trip
  UNIQUE (room_id, event_id)
);

CREATE INDEX idx_messages_room_ts ON messages(room_id, origin_server_ts);
CREATE INDEX idx_messages_ts ON messages(origin_server_ts);
CREATE INDEX idx_messages_sender ON messages(sender);

CREATE VIRTUAL TABLE messages_fts USING fts5(body, room, sender);

CREATE TABLE sync_state (
  key TEXT PRIMARY KEY,             -- 'next_batch', 'last_sync_at', 'access_token_hash', 'user_id'
  value TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);
```

**Things to note**:

- `decrypt_status` is important. Surface missing-key errors to the user in `doctor` and `status` output, with hints about re-exporting keys.
- `raw_json` is useful for v1 features (re-decrypting after a key import, archaeology). Optional — drop if size matters.
- `messages_fts` should not have `body` for `decrypt_status != 'ok'` messages (FTS-on-ciphertext is useless). When inserting, skip FTS rows for failed decrypts.
- **FTS5 does not support UPSERT.** This bit sigcrawl in CI. Use `DELETE FROM messages_fts WHERE rowid=…` followed by `INSERT INTO messages_fts(rowid, …) VALUES (…)` instead of `INSERT … ON CONFLICT`. See sigcrawl's `internal/store/store.go` for the working pattern.

---

## 6. CLI surface

Match the family vocabulary, plus matcrawl-specific additions for login.

```
matcrawl doctor                              # detect existing session, homeserver reachable, keys imported, archive sane
matcrawl login                                # interactive: prompts for homeserver, access token, device ID, key export path + passphrase
matcrawl logout                               # nukes the session + crypto store; archive stays
matcrawl sync                                 # incremental: fetch new events since last sync
matcrawl sync --backfill                      # also backfill historical messages per room
matcrawl status                               # archive counts, oldest/newest event, last sync time, decrypt failure count
matcrawl rooms [--limit N] [--encrypted-only] # list rooms
matcrawl members --room <room_id>             # list room members
matcrawl messages [--room ID] [--sender ID] [--after DATE] [--limit N]
matcrawl search "query" [--room ID] [--limit N]
matcrawl keys import <path-to-keys.txt>       # import additional Megolm keys exported from Element
matcrawl keys retry                            # try to decrypt previously-failed messages (after a new key import)
matcrawl backup init|push|pull|status         # age-encrypted git backup, copy verbatim from sigcrawl
matcrawl metadata                              # crawlkit control manifest
matcrawl version
```

All commands accept `--json` as a global flag (must come before the subcommand — Go stdlib `flag` package limitation that the rest of the family shares; document this in `--help`).

`doctor` should be opinionated. It should detect:
- Whether `~/.matcrawl/` exists and is `0o700`.
- Whether a session is persisted and is still valid (HEAD `/account/whoami`).
- Whether the homeserver is reachable.
- How many rooms are encrypted vs unencrypted, and for the encrypted ones, how many messages currently have `decrypt_status != 'ok'`. Surface this prominently.

---

## 7. File layout

```
matcrawl/
├── cmd/matcrawl/main.go            # thin entry, mirrors sigcrawl
├── internal/
│   ├── cli/
│   │   ├── cli.go                  # subcommand dispatch + flag parsing
│   │   ├── control.go              # crawlkit/control.Manifest
│   │   ├── version.go              # var version; resolveVersion() via runtime/debug.ReadBuildInfo
│   │   └── login.go                # interactive login flow (homeserver URL, token, device, key file)
│   ├── matrix/
│   │   ├── client.go               # mautrix.Client construction; access-token login; persistent session
│   │   ├── crypto.go               # cryptohelper + key import (Element .txt export parser)
│   │   ├── sync.go                 # /sync loop, room state extraction
│   │   ├── backfill.go             # per-room /messages?dir=b paginator with checkpointing
│   │   └── probe.go                # doctor's detection logic
│   ├── store/
│   │   ├── schema.go               # SQL constants
│   │   ├── store.go                # Open/Upsert/Status/ListRooms/Messages/Search
│   │   └── export.go               # Snapshot for backup module
│   └── backup/                     # copy verbatim from sigcrawl/internal/backup
│       ├── backup.go
│       ├── config.go
│       ├── crypto.go
│       └── git.go
├── .github/
│   ├── workflows/
│   │   ├── ci.yml                  # copy from sigcrawl (smoke test commands updated)
│   │   ├── release-please.yml      # copy from sigcrawl verbatim
│   │   ├── pr-title.yml            # copy verbatim
│   │   ├── codeql.yml              # copy verbatim
│   │   └── secret-scan.yml         # copy verbatim
│   ├── CODEOWNERS                  # @peetzweg
│   ├── dependabot.yml              # gomod + github-actions, weekly
│   └── release-drafter.yml         # NOT NEEDED — release-please owns the changelog (sigcrawl dropped this)
├── .goreleaser.yaml                # copy from sigcrawl, swap project_name + module path + homepage; keep brews: block targeting peetzweg/homebrew-tap
├── .release-please-manifest.json   # { ".": "0.1.0" }
├── release-please-config.json      # release-type: go, package-name: matcrawl
├── CHANGELOG.md                    # seed file, release-please fills it
├── Makefile                        # copy from sigcrawl, swap binary name
├── README.md                       # see §11
├── LICENSE                         # MIT, copyright peetzweg
├── .gitignore                      # bin/, *.test, .DS_Store
├── mise.toml                       # [tools]\ngo = "latest"
├── go.mod                          # github.com/peetzweg/matcrawl, go 1.26+, require mautrix-go
└── go.sum
```

---

## 8. Release pipeline — copy verbatim from sigcrawl

This is the most battle-tested piece. We just shipped v0.2.0 with this setup. Copy it literally and only change names.

**Stack:**

- **release-please** (Google's tool) maintains an auto-PR'd "release X.Y.Z" PR on main, driven by conventional-commit prefixes (`feat:` bumps minor, `fix:` bumps patch). When that PR is merged, release-please tags, creates the GitHub Release with a CHANGELOG diff, and the same workflow step then runs GoReleaser to upload cross-compiled binaries and push the formula to the Homebrew tap.
- **GoReleaser** does cross-compile (`darwin_{amd64,arm64}`, `linux_{amd64,arm64}`, `windows_{amd64,arm64}`), archive bundling (.tar.gz + .zip for Windows), checksums, and Homebrew formula generation via its `brews:` block.
- **Homebrew tap** at `peetzweg/homebrew-tap` already exists. The brews block writes `Formula/matcrawl.rb` automatically on every release. Authenticated via a `HOMEBREW_TAP_TOKEN` repo secret (fine-grained PAT with `Contents: read/write` scoped to `peetzweg/homebrew-tap` only).

**Workflow files needed** (only 5 — much leaner than openclaw's 9-workflow stack which we explicitly ripped out as too heavy for a solo non-org repo):

1. `release-please.yml` — single workflow. Runs `googleapis/release-please-action@v4` (NOT v5 — v5 has breaking config-schema changes). If `release_created` is true, then `actions/checkout@v6` + `actions/setup-go@v5` + `goreleaser/goreleaser-action@v6` with `args: release --clean`. Env passes `GITHUB_TOKEN` (auto) + `HOMEBREW_TAP_TOKEN` (secret).
2. `ci.yml` — `go mod verify`, `go mod tidy` diff check, `govulncheck`, gofmt, `go vet`, `go test`, build, CLI smoke test (`--help` banner, `version`, `metadata`, `doctor`).
3. `pr-title.yml` — `amannn/action-semantic-pull-request@v5` enforces conventional-commit PR titles. Necessary because release-please reads PR titles for the changelog (squash-merge means PR title becomes the main commit message).
4. `codeql.yml` — Go security scan weekly + on PR/push.
5. `secret-scan.yml` — TruffleHog verified-secrets gate.

**Do NOT add** unless there's a specific reason:
- `docker.yml` — matcrawl, like sigcrawl, is host-bound. Running a Matrix client in a Linux container that needs to access the user's key export file gains nothing. Skip Docker entirely.
- `homebrew-tap.yml` — the `brews:` block in `.goreleaser.yaml` replaces this. No separate workflow needed.
- `release-drafter.yml` — release-please owns the changelog.
- `auto-assign.yml`, `stale.yml` — solo repo, no need.
- `publish-apt.yml`, `publish-rpm.yml` — Cloudsmith infrastructure required, not free, not needed.

**`.goreleaser.yaml` essentials:**

```yaml
version: 2
project_name: matcrawl

builds:
  - id: matcrawl
    main: ./cmd/matcrawl
    binary: matcrawl
    env: [CGO_ENABLED=0]
    flags: [-trimpath]
    tags: [goolm]            # NB: pure-Go olm
    ldflags:
      - -s -w -X github.com/peetzweg/matcrawl/internal/cli.version={{ .Version }}
    targets:
      - darwin_amd64
      - darwin_arm64
      - linux_amd64
      - linux_arm64
      - windows_amd64
      - windows_arm64

archives:
  - id: bundles
    ids: [matcrawl]
    name_template: "{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}"
    formats: [tar.gz]
    format_overrides:
      - goos: windows
        formats: [zip]
    files: [README.md, LICENSE, CHANGELOG.md]

checksum:
  name_template: "checksums.txt"
  algorithm: sha256

release:
  mode: keep-existing       # release-please owns the release body
  prerelease: auto

changelog:
  disable: true              # release-please owns the changelog

brews:
  - name: matcrawl
    repository:
      owner: peetzweg
      name: homebrew-tap
      branch: main
      token: "{{ .Env.HOMEBREW_TAP_TOKEN }}"
    directory: Formula
    homepage: https://github.com/peetzweg/matcrawl
    description: "Local-first Matrix archive crawler"
    license: MIT
    commit_author:
      name: goreleaserbot
      email: bot@goreleaser.com
    install: |
      bin.install "matcrawl"
    test: |
      assert_match version.to_s, shell_output("#{bin}/matcrawl version")
    caveats: |
      Run `matcrawl login` to authenticate against your Matrix homeserver
      and import your E2E room keys (Element → Settings → Security & Privacy
      → Export E2E room keys).
```

**Pin action versions to match status-saver and sigcrawl** — this is the empirically-working combo. Don't bump to v6 of checkout / v6 of setup-go / v7 of goreleaser-action / v5 of release-please-action without a specific reason:

- `actions/checkout@v6` ✅ (was @v4 in sigcrawl's initial cut, Dependabot bumped, works)
- `actions/setup-go@v6` ✅
- `goreleaser/goreleaser-action@v7` ✅
- `googleapis/release-please-action@v4` ❌ stay on v4. v5 has breaking config-schema changes; we triaged and closed the dependabot v5 bump on sigcrawl.
- `amannn/action-semantic-pull-request@v6` ✅
- `github/codeql-action/{init,analyze}@v4` ✅
- `trufflesecurity/trufflehog@v3.95.3` ✅ (exact pin)

**Required repo secret (one):** `HOMEBREW_TAP_TOKEN` — fine-grained PAT, `Contents: read/write` on `peetzweg/homebrew-tap` only. No expiration is fine if you can rotate when you remember; otherwise 1 year. `GITHUB_TOKEN` is auto-provided by GitHub Actions, no setup needed. **Do not add `CLOUDSMITH_API_KEY`** — sigcrawl explicitly removed all Cloudsmith bits.

---

## 9. Version derivation — don't make sigcrawl's mistake

sigcrawl's first cut hardcoded `var version = "0.1.0"` in `internal/cli/version.go`. The release binaries were correct (GoReleaser injects via `-ldflags "-X .../cli.version={{ .Version }}"`), but **anyone who installed via `go install …@latest` got "0.1.0" forever**, even after v0.2.0 shipped.

Fix from the start:

```go
// internal/cli/version.go
package cli

import "runtime/debug"

// version is injected by GoReleaser at release time via -ldflags
// "-X github.com/peetzweg/matcrawl/internal/cli.version={{ .Version }}".
// Empty in `go install` builds; resolveVersion falls back to the module
// version embedded by the Go toolchain.
var version = ""

func resolveVersion() string {
    if version != "" {
        return version
    }
    if info, ok := debug.ReadBuildInfo(); ok {
        if v := info.Main.Version; v != "" && v != "(devel)" {
            return v
        }
    }
    return "dev"
}
```

Use `resolveVersion()` everywhere you currently print or compare against the version string. Don't reference `version` directly outside `version.go`.

Outcome:
- Release binaries (ldflags injected) → exact tag, e.g. `v0.2.1`.
- `go install …@v0.2.1` → `v0.2.1`.
- `go install …@latest` → latest released tag.
- `go build` from working tree → Go pseudo-version `v0.2.2-0.<date>-<sha>`.
- Anything stranger → `dev`.

This was sigcrawl's only post-release bug. Avoid it.

---

## 10. README layout

```markdown
# matcrawl

[![CI](https://github.com/peetzweg/matcrawl/actions/workflows/ci.yml/badge.svg)](https://github.com/peetzweg/matcrawl/actions/workflows/ci.yml)

Matrix archive CLI. Local-first.

`matcrawl` speaks the Matrix Client-Server API directly. It logs into your
account, decrypts E2EE rooms with your Megolm keys, pulls full message
history, and stores it in a searchable SQLite archive at
`~/.matcrawl/matcrawl.db`. Backup to a private git repo as age-encrypted
shards is optional.

Built on [`mautrix-go`](https://github.com/mautrix/go) and
[`openclaw/crawlkit`](https://github.com/openclaw/crawlkit) — the Matrix
sibling of [`sigcrawl`](https://github.com/peetzweg/sigcrawl) (Signal),
[`telecrawl`](https://github.com/openclaw/telecrawl) (Telegram),
[`wacli`](https://github.com/openclaw/wacli) (WhatsApp), and
[`slacrawl`](https://github.com/openclaw/slacrawl) (Slack). Same CLI
vocabulary across all of them.

## Install

### Homebrew (recommended)

    brew install peetzweg/tap/matcrawl

### Go install

    go install github.com/peetzweg/matcrawl/cmd/matcrawl@latest

### Build from source

    git clone https://github.com/peetzweg/matcrawl
    cd matcrawl
    make build

## First-time setup

1. **Find your access token in Element**:
   Settings → Help & About → Advanced → "Access Token". Copy it.
2. **Export your E2E room keys from Element**:
   Settings → Security & Privacy → "Export E2E room keys". Pick a passphrase. Save the `.txt` file.
3. **Run matcrawl login**:

       matcrawl login

   It will prompt for your homeserver URL (e.g. `https://matrix.org`),
   access token, device ID, key export path, and the export passphrase.

4. **Sync**:

       matcrawl doctor
       matcrawl sync --backfill   # initial full backfill, takes a while
       matcrawl status

Subsequent runs of `sync` are incremental.

## Read

    matcrawl rooms --limit 20
    matcrawl messages --room '!abc:server.example' --after 2026-01-01
    matcrawl messages --media
    matcrawl search "<query>"

All commands accept `--json` for machine-readable output.

## Backup (optional)

    matcrawl backup init --repo ~/Projects/backup-matcrawl --remote git@github.com:you/backup-matcrawl.git
    matcrawl backup push

The repository contains only age-encrypted `.jsonl.gz.age` shards.

## E2EE caveats (v0)

matcrawl decrypts encrypted rooms using the keys you export from Element.
If you join a new encrypted room *after* exporting keys, you must re-run
the export and call `matcrawl keys import keys.txt`.

The v1 cross-signing flow will eliminate this step.

## Not supported

- Matrix Spaces hierarchy preservation (you get room data, not spatial structure)
- Direct media re-download from mxc URIs (paths recorded, blobs not fetched — planned for v1)
- iOS/Android Element backup files (no comparable format exists)

## License

MIT
```

---

## 11. Gotchas from sigcrawl development — read this before you start

These bit sigcrawl in CI and are easy to avoid if you know about them:

1. **FTS5 does not support `INSERT … ON CONFLICT … DO UPDATE`.** Use `DELETE FROM messages_fts WHERE rowid=…; INSERT INTO messages_fts(rowid, …) VALUES (…)`. Sigcrawl's `internal/store/store.go` `Upsert()` has the working pattern.
2. **The `version.go` hardcode trap.** See §9. Fix it from day one.
3. **`gofmt -l .` is a CI failure in the lint job.** Always run `gofmt -s -w .` before pushing. Sigcrawl's first CI run failed on this.
4. **Action version pins matter.** GitHub's `codeload.github.com` had an outage during sigcrawl's CI bring-up that 404'd random action SHAs. The combo of `actions/checkout@v6` + `actions/setup-go@v6` initially failed but is fine now. Document the working versions in `.github/workflows/` (see §8) and let Dependabot bump them with CI gating.
5. **Brand-new repos sometimes need their first workflow run to be triggered manually.** Sigcrawl's first push didn't fire any runs; an empty commit kicked them. If you push your initial commits and nothing runs, push one more commit to break the gate.
6. **`go install` puts binaries in `$GOPATH/bin`, which is not on most users' `$PATH` by default.** Document `export PATH="$HOME/go/bin:$PATH"` in the README under "Go install".
7. **release-please starts at the version in `.release-please-manifest.json`, not 0.0.0.** Seed it at `0.1.0` (matching `internal/cli/version.go`'s implicit dev value).
8. **The brews: block requires the formula directory to exist on the tap repo.** `peetzweg/homebrew-tap` already has `Formula/`. If you start a *new* tap, init it with a `Formula/` dir + a stub file or goreleaser will fail to write.
9. **PR titles must be conventional commits.** `pr-title.yml` enforces this. Document recognized types: `feat`, `fix`, `chore`, `docs`, `refactor`, `test`, `ci`, `build`, `perf`, `revert`. Dependabot's `build(deps): bump X` titles match `build`, so they pass.
10. **Major action bumps from Dependabot deserve manual triage.** sigcrawl closed `release-please-action 4→5` because v5 has breaking schema changes. Don't auto-merge majors blindly.
11. **`actions/checkout`'s default `fetch-depth: 1` is too shallow for GoReleaser.** Set `fetch-depth: 0` on the checkout step inside `release-please.yml`.
12. **Pure-Go SQLite via `modernc.org/sqlite` has a quirk**: `sql.Open("sqlite", dsn)` not `sql.Open("sqlite3", dsn)`. Driver name is `sqlite`, not `sqlite3`. Bit telecrawl and sigcrawl during early dev.

---

## 12. Verification plan

End-to-end checks for the agent implementing this:

1. **Local build clean.** `make build && ./bin/matcrawl --help` shows the banner; `./bin/matcrawl version` returns a non-empty string; `./bin/matcrawl --json metadata | jq '.schema_version'` returns `"crawlkit.control.v1"`.
2. **Doctor on cold install.** `./bin/matcrawl doctor` runs cleanly with no session and reports a useful "no session — run `matcrawl login`" message.
3. **Login E2E** (requires a real Matrix account):
   - `matcrawl login` against matrix.org → prompts for token, device ID, key file path, passphrase.
   - Session persisted in `~/.matcrawl/session.json` (or wherever you put it).
   - Crypto store populated in `~/.matcrawl/crypto.db`.
4. **Initial backfill.** `matcrawl sync --backfill` for at least one E2EE and one plaintext room. `matcrawl status` reports correct totals and zero decrypt failures (assuming keys are present).
5. **Decryption coverage.** Pick a known E2EE room with attachments + reactions + a thread; verify `matcrawl messages --room <id> --json` returns rich rows with `decrypt_status: "ok"`.
6. **Incremental sync.** Send a new message from another Matrix client; re-run `matcrawl sync`; confirm the new message appears.
7. **Search.** `matcrawl search "<known phrase>" --json` returns FTS5 results with snippet highlighting.
8. **Backup roundtrip.** `matcrawl backup init --repo /tmp/backup-test` + `matcrawl backup push` (no remote) + verify `.age` shards + manifest.json on disk + `matcrawl backup pull` into a clean dir.
9. **CI green on first PR.** All 5 workflows pass.
10. **First release ships.** Merge a `feat:` to main → release-please opens a "release 0.1.0" PR → merge it → v0.1.0 tag + GitHub release + 6 binaries uploaded + `Formula/matcrawl.rb` appears on `peetzweg/homebrew-tap`.
11. **Brew install from tap actually works.** On a clean macOS shell: `brew install peetzweg/tap/matcrawl && matcrawl version` → `v0.1.0`.

---

## 13. References

### Matrix / mautrix
- [mautrix/go (the SDK)](https://github.com/mautrix/go) — pkg.go.dev: https://pkg.go.dev/maunium.net/go/mautrix
- [mautrix/crypto (Olm/Megolm)](https://pkg.go.dev/maunium.net/go/mautrix/crypto)
- [mautrix/crypto/cryptohelper](https://pkg.go.dev/maunium.net/go/mautrix/crypto/cryptohelper)
- [August 2025 mautrix release notes](https://mau.fi/blog/2025-08-mautrix-release/)
- [Matrix Client-Server API spec](https://spec.matrix.org/latest/client-server-api/) — esp. the "Key exports" section for the Element `.txt` format
- [Synapse `/messages` performance issue (slow backfill)](https://github.com/matrix-org/synapse/issues/13356)
- [matrix-org/seshat (Element's FTS index — for awareness, not consumption)](https://github.com/matrix-org/seshat)
- [matrix-org/pantalaimon (archived 2026-04-08 — do not use)](https://github.com/matrix-org/pantalaimon)

### Reference implementations to crib from
- [gomuks (full Matrix TUI client in mautrix-go)](https://github.com/gomuks/gomuks) — best Go E2EE example
- [russelldavies/matrix-archive (Python, key-export UX)](https://github.com/russelldavies/matrix-archive) — copy the `.txt` key import flow
- [MeowOrange/matrix-archive (Python, SQLite output)](https://github.com/MeowOrange/matrix-archive) — schema ideas
- [8go/matrix-commander](https://github.com/8go/matrix-commander) — comprehensive Matrix CLI in Python; UX reference

### Family + infrastructure
- [peetzweg/sigcrawl](https://github.com/peetzweg/sigcrawl) — the reference implementation. Lift its `internal/backup/`, `internal/store/` patterns, `.github/workflows/`, `.goreleaser.yaml`, `release-please-config.json`.
- [peetzweg/homebrew-tap](https://github.com/peetzweg/homebrew-tap) — existing tap. Formula will land here.
- [openclaw/crawlkit](https://github.com/openclaw/crawlkit) — shared library. Use `crawlkit/control` for the manifest.
- [openclaw/telecrawl](https://github.com/openclaw/telecrawl), [openclaw/slacrawl](https://github.com/openclaw/slacrawl), [openclaw/wacli](https://github.com/openclaw/wacli) — sibling implementations to align with.
- [Release-please action](https://github.com/googleapis/release-please-action) (pin **@v4**, not v5)
- [GoReleaser](https://goreleaser.com/)

### Lessons-learned cross-reference
- sigcrawl's commit history at https://github.com/peetzweg/sigcrawl/commits/main is short and instructive. The post-scaffold fixes (gofmt, FTS5 upsert, version derivation, docker removal) are 5 commits each <20 LOC. Read them.

---

## 14. Out of scope for v0

- **Matrix Spaces** as first-class objects. Treat the parent space's rooms as plain rooms.
- **Re-downloading media blobs from mxc URIs.** Record paths in `attachments_json`, leave the actual fetch to v1.
- **Sending messages.** matcrawl is read-only. If a user wants to send via CLI, they use [matrix-commander](https://github.com/8go/matrix-commander) — orthogonal.
- **Bridged conversations (Telegram/Signal/etc. via mautrix bridges).** They show up as normal rooms; matcrawl archives them like any other room. No special handling.
- **Multiple accounts.** v0 supports exactly one account. Multiple-account support belongs in v2.
- **iOS / Android Element export.** No comparable export format exists.

---

## 15. Suggested first PR / commit sequence

1. `feat: scaffold matcrawl module + cmd entrypoint` — `go mod init`, `cmd/matcrawl/main.go`, `internal/cli/{cli,control,version}.go`, `Makefile`, `LICENSE`, `.gitignore`, `mise.toml`. Don't depend on mautrix yet; just get `matcrawl --help` to build.
2. `feat: store schema + open/upsert/status/messages/search` — `internal/store/`. SQLite + FTS5.
3. `feat: mautrix client + access-token login` — `internal/matrix/client.go`. Bare auth, no E2EE yet.
4. `feat: sync + backfill` — `internal/matrix/sync.go`, `backfill.go`. Plaintext rooms only.
5. `feat: E2EE key import + decryption` — `internal/matrix/crypto.go`. The Element `.txt` parser + `OlmMachine` integration.
6. `feat: backup module` — copy verbatim from sigcrawl, adapt for matcrawl's schema.
7. `ci: release-please + goreleaser + brew tap` — copy from sigcrawl, swap names.
8. `docs: README + first release` — merge → release-please opens v0.1.0 PR → merge → release.

Each of those is mergeable independently. CI will gate them. The brews block won't be exercised until step 7's first release, but you can verify it locally with `goreleaser release --snapshot --clean --skip=publish` before pushing.

---

**End of brief. Good luck — and pay attention to §11 (gotchas) before debugging anything.**
