package signaldesktop

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/peetzweg/sigcrawl/internal/store"
)

type ImportOptions struct {
	Path           string
	Sigtop         string
	IncludeMembers bool
}

type ImportResult struct {
	Stats    store.ImportStats
	Chats    []store.Chat
	Messages []store.Message
}

func Import(ctx context.Context, opts ImportOptions, archivePath string) (ImportResult, error) {
	dir := strings.TrimSpace(opts.Path)
	if dir == "" {
		dir = DefaultPath()
	}
	sigtopBin, err := ResolveSigtop(opts.Sigtop)
	if err != nil {
		return ImportResult{}, fmt.Errorf("sigtop binary not found in PATH (install with `brew install tbvdm/tap/sigtop`): %w", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sql", "db.sqlite")); err != nil {
		return ImportResult{}, fmt.Errorf("signal desktop db not found at %s: %w", filepath.Join(dir, "sql", "db.sqlite"), err)
	}
	started := time.Now().UTC()

	tmpDir, err := os.MkdirTemp(filepath.Dir(archivePath), "sigcrawl-dump-*")
	if err != nil {
		return ImportResult{}, fmt.Errorf("mkdtemp: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	plain := filepath.Join(tmpDir, "signal.sqlite")
	cmd := exec.CommandContext(ctx, sigtopBin, "export-database", "-d", dir, plain)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return ImportResult{}, fmt.Errorf("sigtop export-database: %w", err)
	}

	chats, messages, err := readPlain(ctx, plain)
	if err != nil {
		return ImportResult{}, err
	}

	stats := store.ImportStats{
		SourcePath: dir,
		DBPath:     archivePath,
		Chats:      len(chats),
		Messages:   len(messages),
		StartedAt:  started,
		FinishedAt: time.Now().UTC(),
	}
	for _, m := range messages {
		if m.HasAttachments {
			stats.MediaMessages++
		}
	}
	return ImportResult{Stats: stats, Chats: chats, Messages: messages}, nil
}

func readPlain(ctx context.Context, plainPath string) ([]store.Chat, []store.Message, error) {
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(5000)", plainPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("open plaintext: %w", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.PingContext(ctx); err != nil {
		return nil, nil, fmt.Errorf("ping plaintext: %w", err)
	}
	chats, idxByID, err := readChats(ctx, db)
	if err != nil {
		return nil, nil, err
	}
	messages, err := readMessages(ctx, db, chats, idxByID)
	if err != nil {
		return nil, nil, err
	}
	return chats, messages, nil
}

func readChats(ctx context.Context, db *sql.DB) ([]store.Chat, map[string]int, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT
			c.id,
			c.type,
			c.name,
			c.profileFullName,
			c.e164,
			c.serviceId,
			c.groupId
		FROM conversations c
	`)
	if err != nil {
		return nil, nil, fmt.Errorf("query conversations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []store.Chat
	byID := map[string]int{}
	for rows.Next() {
		var (
			id, kind                                string
			name, profile, e164, serviceID, groupID sql.NullString
		)
		if err := rows.Scan(&id, &kind, &name, &profile, &e164, &serviceID, &groupID); err != nil {
			return nil, nil, err
		}
		c := store.Chat{
			ID:        id,
			Kind:      kind,
			Name:      firstNonEmpty(name.String, profile.String),
			E164:      e164.String,
			ServiceID: serviceID.String,
			GroupID:   groupID.String,
		}
		byID[id] = len(out)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return out, byID, nil
}

func readMessages(ctx context.Context, db *sql.DB, chats []store.Chat, idxByID map[string]int) ([]store.Message, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT
			m.id,
			m.conversationId,
			m.type,
			m.body,
			m.sent_at,
			COALESCE(m.received_at_ms, m.received_at) AS recv_ts,
			m.hasAttachments,
			c.id AS source_conv_id,
			(SELECT json_group_array(json_object(
				'type', attachmentType,
				'contentType', contentType,
				'fileName', fileName,
				'path', path,
				'size', size,
				'caption', caption,
				'height', height,
				'width', width
			)) FROM message_attachments a WHERE a.messageId = m.id AND a.attachmentType = 'attachment') AS attachments_json,
			(SELECT json_group_array(json_object(
				'emoji', emoji,
				'fromId', fromId,
				'timestamp', timestamp
			)) FROM reactions r WHERE r.messageId = m.id) AS reactions_json,
			(SELECT json_group_array(json_object(
				'mentionAci', mentionAci,
				'start', start,
				'length', length
			)) FROM mentions men WHERE men.messageId = m.id) AS mentions_json
		FROM messages m
		LEFT JOIN conversations c ON m.sourceServiceId = c.serviceId
		ORDER BY m.sent_at ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []store.Message
	for rows.Next() {
		var (
			id, chatID                                   string
			typeStr, body, sourceID                      sql.NullString
			attachmentsJSON, reactionsJSON, mentionsJSON sql.NullString
			sentAt, recvAt                               sql.NullInt64
			hasAttach                                    sql.NullInt64
		)
		if err := rows.Scan(&id, &chatID, &typeStr, &body, &sentAt, &recvAt, &hasAttach, &sourceID, &attachmentsJSON, &reactionsJSON, &mentionsJSON); err != nil {
			return nil, err
		}
		msg := store.Message{
			MessageID:      id,
			ChatID:         chatID,
			SourceID:       sourceID.String,
			Type:           typeStr.String,
			Body:           body.String,
			Timestamp:      msToTime(sentAt.Int64),
			ReceivedAt:     msToTime(recvAt.Int64),
			FromMe:         typeStr.String == "outgoing",
			HasAttachments: hasAttach.Int64 != 0,
		}
		if !isEmptyJSONArray(attachmentsJSON.String) {
			msg.AttachmentsJSON = attachmentsJSON.String
		}
		if !isEmptyJSONArray(reactionsJSON.String) {
			msg.ReactionsJSON = reactionsJSON.String
		}
		if !isEmptyJSONArray(mentionsJSON.String) {
			msg.MentionsJSON = mentionsJSON.String
		}
		if idx, ok := idxByID[chatID]; ok {
			msg.ChatName = chats[idx].Name
			msg.ChatKind = chats[idx].Kind
			if msg.Timestamp.After(chats[idx].LastMessageAt) {
				chats[idx].LastMessageAt = msg.Timestamp
			}
			chats[idx].MessageCount++
		}
		if idx, ok := idxByID[sourceID.String]; ok {
			msg.SenderName = chats[idx].Name
		}
		out = append(out, msg)
	}
	return out, rows.Err()
}

func isEmptyJSONArray(s string) bool {
	t := strings.TrimSpace(s)
	return t == "" || t == "[]" || t == "null"
}

func msToTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
