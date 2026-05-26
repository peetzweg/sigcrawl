package store

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type Snapshot struct {
	SchemaVersion int       `json:"schema_version"`
	ExportedAt    time.Time `json:"exported_at"`
	Chats         []Chat    `json:"chats"`
	Messages      []Message `json:"messages"`
}

func (s *Store) ExportAll(ctx context.Context) (Snapshot, error) {
	out := Snapshot{SchemaVersion: schemaVersion, ExportedAt: time.Now().UTC()}

	chats, err := s.ListChats(ctx, 1_000_000)
	if err != nil {
		return out, fmt.Errorf("export chats: %w", err)
	}
	out.Chats = chats

	rows, err := s.db.QueryContext(ctx, `
		select chat_id, message_id, coalesce(source_id,''), coalesce(chat_name,''), coalesce(chat_kind,''),
		       coalesce(sender_name,''), ts, coalesce(recv_ts,0), from_me, coalesce(body,''),
		       coalesce(type,''), has_attachments, coalesce(attachments_json,''),
		       coalesce(reactions_json,''), coalesce(mentions_json,''), coalesce(quote_json,''),
		       coalesce(edit_history_json,''), coalesce(raw_json,'')
		from messages order by ts asc
	`)
	if err != nil {
		return out, fmt.Errorf("export messages: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var m Message
		var ts, recvTs int64
		var fromMe, hasAttach int
		if err := rows.Scan(
			&m.ChatID, &m.MessageID, &m.SourceID, &m.ChatName, &m.ChatKind,
			&m.SenderName, &ts, &recvTs, &fromMe, &m.Body,
			&m.Type, &hasAttach, &m.AttachmentsJSON,
			&m.ReactionsJSON, &m.MentionsJSON, &m.QuoteJSON,
			&m.EditHistoryJSON, &m.RawJSON,
		); err != nil {
			return out, err
		}
		m.Timestamp = fromUnix(ts)
		m.ReceivedAt = fromUnix(recvTs)
		m.FromMe = fromMe != 0
		m.HasAttachments = hasAttach != 0
		out.Messages = append(out.Messages, m)
	}
	return out, rows.Err()
}

func (snap Snapshot) Validate() error {
	if snap.SchemaVersion == 0 {
		return errors.New("snapshot missing schema_version")
	}
	for _, m := range snap.Messages {
		if m.MessageID == "" || m.ChatID == "" {
			return fmt.Errorf("message missing chat_id/message_id: %+v", m)
		}
	}
	return nil
}

func (s *Store) ImportSnapshot(ctx context.Context, snap Snapshot, source string, exported time.Time) error {
	stats := ImportStats{SourcePath: source, DBPath: s.path, Chats: len(snap.Chats), Messages: len(snap.Messages), StartedAt: exported, FinishedAt: time.Now().UTC()}
	return s.Upsert(ctx, stats, snap.Chats, snap.Messages)
}
