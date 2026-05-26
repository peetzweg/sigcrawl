package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schemaVersion = 1

type Store struct {
	db   *sql.DB
	path string
}

type ImportStats struct {
	SourcePath    string    `json:"source_path"`
	DBPath        string    `json:"db_path"`
	Chats         int       `json:"chats"`
	Messages      int       `json:"messages"`
	MediaMessages int       `json:"media_messages"`
	StartedAt     time.Time `json:"started_at"`
	FinishedAt    time.Time `json:"finished_at"`
}

type Status struct {
	DBPath         string    `json:"db_path"`
	Chats          int       `json:"chats"`
	Messages       int       `json:"messages"`
	MediaMessages  int       `json:"media_messages"`
	OldestMessage  time.Time `json:"oldest_message,omitzero"`
	NewestMessage  time.Time `json:"newest_message,omitzero"`
	LastImportAt   time.Time `json:"last_import_at,omitzero"`
	LastSource     string    `json:"last_source,omitempty"`
}

type Chat struct {
	ID            string    `json:"id"`
	Kind          string    `json:"kind"`
	Name          string    `json:"name,omitempty"`
	E164          string    `json:"e164,omitempty"`
	ServiceID     string    `json:"service_id,omitempty"`
	GroupID       string    `json:"group_id,omitempty"`
	LastMessageAt time.Time `json:"last_message_at,omitzero"`
	UnreadCount   int       `json:"unread_count"`
	MessageCount  int       `json:"message_count"`
}

type Message struct {
	ChatID          string    `json:"chat_id"`
	MessageID       string    `json:"message_id"`
	SourceID        string    `json:"source_id,omitempty"`
	ChatName        string    `json:"chat_name,omitempty"`
	ChatKind        string    `json:"chat_kind,omitempty"`
	SenderName      string    `json:"sender_name,omitempty"`
	Timestamp       time.Time `json:"timestamp"`
	ReceivedAt      time.Time `json:"received_at,omitzero"`
	FromMe          bool      `json:"from_me"`
	Body            string    `json:"body,omitempty"`
	Type            string    `json:"type,omitempty"`
	HasAttachments  bool      `json:"has_attachments,omitempty"`
	AttachmentsJSON string    `json:"attachments_json,omitempty"`
	ReactionsJSON   string    `json:"reactions_json,omitempty"`
	MentionsJSON    string    `json:"mentions_json,omitempty"`
	QuoteJSON       string    `json:"quote_json,omitempty"`
	EditHistoryJSON string    `json:"edit_history_json,omitempty"`
	RawJSON         string    `json:"raw_json,omitempty"`
	Snippet         string    `json:"snippet,omitempty"`
}

type MessageFilter struct {
	Query    string
	ChatID   string
	Sender   string
	Limit    int
	After    *time.Time
	Before   *time.Time
	FromMe   *bool
	HasMedia bool
	Asc      bool
}

func Open(ctx context.Context, path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("db path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("mkdir db dir: %w", err)
	}
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	s := &Store{db: db, path: path}
	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf("pragma user_version = %d", schemaVersion)); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Path() string { return s.path }
func (s *Store) DB() *sql.DB  { return s.db }

func (s *Store) Upsert(ctx context.Context, stats ImportStats, chats []Chat, messages []Message) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)

	for _, c := range chats {
		if _, err := tx.ExecContext(ctx, `
			insert into chats(id,kind,name,e164,service_id,group_id,last_message_at,unread_count,message_count)
			values(?,?,?,?,?,?,?,?,?)
			on conflict(id) do update set
				kind=excluded.kind,
				name=excluded.name,
				e164=excluded.e164,
				service_id=excluded.service_id,
				group_id=excluded.group_id,
				last_message_at=case when excluded.last_message_at > coalesce(chats.last_message_at,0) then excluded.last_message_at else chats.last_message_at end,
				message_count=excluded.message_count
		`, c.ID, c.Kind, c.Name, c.E164, c.ServiceID, c.GroupID, unix(c.LastMessageAt), c.UnreadCount, c.MessageCount); err != nil {
			return fmt.Errorf("upsert chat %s: %w", c.ID, err)
		}
	}

	for _, m := range messages {
		res, err := tx.ExecContext(ctx, `
			insert into messages(chat_id,message_id,source_id,chat_name,chat_kind,sender_name,ts,recv_ts,from_me,body,type,has_attachments,attachments_json,reactions_json,mentions_json,quote_json,edit_history_json,raw_json)
			values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
			on conflict(chat_id,message_id) do update set
				body=excluded.body,
				type=excluded.type,
				attachments_json=excluded.attachments_json,
				reactions_json=excluded.reactions_json,
				mentions_json=excluded.mentions_json,
				quote_json=excluded.quote_json,
				edit_history_json=excluded.edit_history_json,
				raw_json=excluded.raw_json
		`, m.ChatID, m.MessageID, m.SourceID, m.ChatName, m.ChatKind, m.SenderName, unix(m.Timestamp), unix(m.ReceivedAt), boolInt(m.FromMe), m.Body, m.Type, boolInt(m.HasAttachments), m.AttachmentsJSON, m.ReactionsJSON, m.MentionsJSON, m.QuoteJSON, m.EditHistoryJSON, m.RawJSON)
		if err != nil {
			return fmt.Errorf("upsert message %s/%s: %w", m.ChatID, m.MessageID, err)
		}
		_ = res
		ftsBody := strings.TrimSpace(m.Body)
		if _, err := tx.ExecContext(ctx,
			`delete from messages_fts where rowid=(select rowid from messages where chat_id=? and message_id=?)`,
			m.ChatID, m.MessageID); err != nil {
			return fmt.Errorf("fts delete: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`insert into messages_fts(rowid,body,chat,sender) values((select rowid from messages where chat_id=? and message_id=?),?,?,?)`,
			m.ChatID, m.MessageID, ftsBody, m.ChatName, m.SenderName); err != nil {
			return fmt.Errorf("fts insert: %w", err)
		}
	}

	now := stats.FinishedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	for key, value := range map[string]string{
		"last_import_at": now.Format(time.RFC3339Nano),
		"source_path":    stats.SourcePath,
	} {
		if _, err := tx.ExecContext(ctx, `
			insert into sync_state(key,value,updated_at) values(?,?,?)
			on conflict(key) do update set value=excluded.value, updated_at=excluded.updated_at
		`, key, value, unix(now)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Status(ctx context.Context) (Status, error) {
	out := Status{DBPath: s.path}
	for _, c := range []struct {
		dst *int
		q   string
	}{
		{&out.Chats, "select count(*) from chats"},
		{&out.Messages, "select count(*) from messages"},
		{&out.MediaMessages, "select count(*) from messages where has_attachments <> 0"},
	} {
		if err := s.db.QueryRowContext(ctx, c.q).Scan(c.dst); err != nil {
			return out, err
		}
	}
	var oldest, newest sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `select min(ts), max(ts) from messages`).Scan(&oldest, &newest); err != nil {
		return out, err
	}
	if oldest.Valid {
		out.OldestMessage = fromUnix(oldest.Int64)
	}
	if newest.Valid {
		out.NewestMessage = fromUnix(newest.Int64)
	}
	var lastImport string
	_ = s.db.QueryRowContext(ctx, `select value from sync_state where key='last_import_at'`).Scan(&lastImport)
	if t, err := time.Parse(time.RFC3339Nano, lastImport); err == nil {
		out.LastImportAt = t
	}
	_ = s.db.QueryRowContext(ctx, `select value from sync_state where key='source_path'`).Scan(&out.LastSource)
	return out, nil
}

func (s *Store) ListChats(ctx context.Context, limit int) ([]Chat, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		select id, kind, coalesce(name,''), coalesce(e164,''), coalesce(service_id,''), coalesce(group_id,''),
		       coalesce(last_message_at,0), unread_count, message_count
		from chats
		order by last_message_at desc nulls last
		limit ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Chat
	for rows.Next() {
		var c Chat
		var ts int64
		if err := rows.Scan(&c.ID, &c.Kind, &c.Name, &c.E164, &c.ServiceID, &c.GroupID, &ts, &c.UnreadCount, &c.MessageCount); err != nil {
			return nil, err
		}
		c.LastMessageAt = fromUnix(ts)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) Messages(ctx context.Context, filter MessageFilter) ([]Message, error) {
	return s.messages(ctx, filter, false)
}

func (s *Store) Search(ctx context.Context, filter MessageFilter) ([]Message, error) {
	if strings.TrimSpace(filter.Query) == "" {
		return nil, errors.New("search query required")
	}
	return s.messages(ctx, filter, true)
}

func (s *Store) messages(ctx context.Context, filter MessageFilter, search bool) ([]Message, error) {
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	var query string
	args := []any{}
	prefix := ""
	if search {
		query = `
			select m.chat_id, m.message_id, coalesce(m.source_id,''), coalesce(m.chat_name,''), coalesce(m.chat_kind,''),
			       coalesce(m.sender_name,''), m.ts, coalesce(m.recv_ts,0), m.from_me, coalesce(m.body,''),
			       coalesce(m.type,''), m.has_attachments, coalesce(m.attachments_json,''),
			       coalesce(m.reactions_json,''), coalesce(m.mentions_json,''), coalesce(m.quote_json,''),
			       coalesce(m.edit_history_json,''),
			       snippet(messages_fts,0,'[',']','…',12) as snippet
			from messages_fts f
			join messages m on m.rowid = f.rowid
			where messages_fts match ?
		`
		args = append(args, filter.Query)
		prefix = "m."
	} else {
		query = `
			select chat_id, message_id, coalesce(source_id,''), coalesce(chat_name,''), coalesce(chat_kind,''),
			       coalesce(sender_name,''), ts, coalesce(recv_ts,0), from_me, coalesce(body,''),
			       coalesce(type,''), has_attachments, coalesce(attachments_json,''),
			       coalesce(reactions_json,''), coalesce(mentions_json,''), coalesce(quote_json,''),
			       coalesce(edit_history_json,''), '' as snippet
			from messages where 1=1
		`
	}
	if filter.ChatID != "" {
		query += " and " + prefix + "chat_id = ?"
		args = append(args, filter.ChatID)
	}
	if filter.Sender != "" {
		query += " and " + prefix + "source_id = ?"
		args = append(args, filter.Sender)
	}
	if filter.After != nil {
		query += " and " + prefix + "ts >= ?"
		args = append(args, unix(*filter.After))
	}
	if filter.Before != nil {
		query += " and " + prefix + "ts <= ?"
		args = append(args, unix(*filter.Before))
	}
	if filter.FromMe != nil {
		query += " and " + prefix + "from_me = ?"
		args = append(args, boolInt(*filter.FromMe))
	}
	if filter.HasMedia {
		query += " and " + prefix + "has_attachments <> 0"
	}
	switch {
	case search:
		query += " order by bm25(messages_fts) limit ?"
	case filter.Asc:
		query += " order by ts asc limit ?"
	default:
		query += " order by ts desc limit ?"
	}
	args = append(args, filter.Limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Message
	for rows.Next() {
		var m Message
		var ts, recvTs int64
		var fromMe, hasAttach int
		if err := rows.Scan(
			&m.ChatID, &m.MessageID, &m.SourceID, &m.ChatName, &m.ChatKind,
			&m.SenderName, &ts, &recvTs, &fromMe, &m.Body,
			&m.Type, &hasAttach, &m.AttachmentsJSON,
			&m.ReactionsJSON, &m.MentionsJSON, &m.QuoteJSON,
			&m.EditHistoryJSON, &m.Snippet,
		); err != nil {
			return nil, err
		}
		m.Timestamp = fromUnix(ts)
		m.ReceivedAt = fromUnix(recvTs)
		m.FromMe = fromMe != 0
		m.HasAttachments = hasAttach != 0
		out = append(out, m)
	}
	return out, rows.Err()
}

func rollback(tx *sql.Tx) {
	_ = tx.Rollback()
}

func unix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func fromUnix(s int64) time.Time {
	if s == 0 {
		return time.Time{}
	}
	return time.Unix(s, 0).UTC()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
