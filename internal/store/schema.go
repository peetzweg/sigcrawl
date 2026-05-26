package store

const schemaSQL = `
create table if not exists chats (
	id text primary key,
	kind text not null,
	name text,
	e164 text,
	service_id text,
	group_id text,
	last_message_at integer,
	unread_count integer not null default 0,
	message_count integer not null default 0
);

create table if not exists messages (
	rowid integer primary key autoincrement,
	chat_id text not null,
	message_id text not null,
	source_id text,
	chat_name text,
	chat_kind text,
	sender_name text,
	ts integer not null,
	recv_ts integer,
	from_me integer not null default 0,
	body text,
	type text,
	has_attachments integer not null default 0,
	attachments_json text,
	reactions_json text,
	mentions_json text,
	quote_json text,
	edit_history_json text,
	raw_json text,
	unique(chat_id, message_id)
);

create index if not exists idx_messages_chat_ts on messages(chat_id, ts);
create index if not exists idx_messages_ts on messages(ts);
create index if not exists idx_messages_from_me on messages(from_me);

create virtual table if not exists messages_fts using fts5(body, chat, sender);

create table if not exists sync_state (
	key text primary key,
	value text not null,
	updated_at integer not null
);
`
