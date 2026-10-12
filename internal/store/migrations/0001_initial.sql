-- SPDX-License-Identifier: GPL-3.0-or-later
-- Initial schema (docs/architecture/data-model.md §3). Times are Unix
-- milliseconds. Text columns hold raw, untrusted content. IDs of
-- identities, chats and messages are never reused (AUTOINCREMENT): the
-- UI caches names and rendered messages by ID.

-- One row per person. The LID is canonical once known (ADR-0009).
CREATE TABLE identities (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  lid           TEXT UNIQUE,
  pn            TEXT UNIQUE,
  contact_name  TEXT,
  push_name     TEXT,
  business_name TEXT,
  is_me         INTEGER NOT NULL DEFAULT 0,
  updated_at    INTEGER NOT NULL,
  CHECK (lid IS NOT NULL OR pn IS NOT NULL)
) STRICT;

-- Every JID seen, PN or LID, maps to one identity.
CREATE TABLE jid_aliases (
  jid          TEXT PRIMARY KEY,
  identity_id  INTEGER NOT NULL REFERENCES identities(id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;

-- Conversations: never contacts.
CREATE TABLE chats (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  jid               TEXT NOT NULL UNIQUE,
  kind              TEXT NOT NULL CHECK (kind IN ('dm', 'group', 'broadcast', 'newsletter')),
  peer_identity_id  INTEGER REFERENCES identities(id),
  title             TEXT,
  last_message_id   INTEGER,
  last_activity_ts  INTEGER NOT NULL DEFAULT 0,
  unread_count      INTEGER NOT NULL DEFAULT 0,
  last_read_ts      INTEGER NOT NULL DEFAULT 0,
  pinned_at         INTEGER,
  archived          INTEGER NOT NULL DEFAULT 0,
  muted_until       INTEGER,
  ephemeral_secs    INTEGER,
  history_complete  INTEGER NOT NULL DEFAULT 0,
  updated_at        INTEGER NOT NULL
) STRICT;
CREATE INDEX chats_list ON chats (archived, pinned_at DESC, last_activity_ts DESC);

-- The rowid is the local, monotonic sort key and the FTS rowid.
CREATE TABLE messages (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  chat_id       INTEGER NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
  wa_id         TEXT NOT NULL,
  sender_id     INTEGER REFERENCES identities(id),
  from_me       INTEGER NOT NULL,
  ts            INTEGER NOT NULL,
  kind          TEXT NOT NULL,
  body          TEXT,
  quoted_wa_id  TEXT,
  status        INTEGER NOT NULL,
  edited_at     INTEGER,
  revoked_at    INTEGER,
  flags         INTEGER NOT NULL DEFAULT 0,
  revision      INTEGER NOT NULL DEFAULT 0,
  raw           BLOB,
  UNIQUE (chat_id, wa_id)
) STRICT;
CREATE INDEX messages_chat_ts ON messages (chat_id, ts);
-- Identity merges repoint senders; without this they would scan every message.
CREATE INDEX messages_sender ON messages (sender_id);

CREATE TABLE media (
  message_id   INTEGER PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
  mime         TEXT,
  size         INTEGER,
  file_name    TEXT,
  width        INTEGER,
  height       INTEGER,
  duration_ms  INTEGER,
  sha256       BLOB,
  state        INTEGER NOT NULL DEFAULT 0,
  local_name   TEXT,
  updated_at   INTEGER NOT NULL
) STRICT;

CREATE TABLE reactions (
  chat_id       INTEGER NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
  target_wa_id  TEXT NOT NULL,
  sender_id     INTEGER NOT NULL REFERENCES identities(id),
  emoji         TEXT NOT NULL,
  ts            INTEGER NOT NULL,
  PRIMARY KEY (chat_id, target_wa_id, sender_id)
) STRICT, WITHOUT ROWID;

-- Edits, revokes and receipts whose target message has not arrived yet.
CREATE TABLE pending_ops (
  id            INTEGER PRIMARY KEY,
  chat_id       INTEGER NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
  target_wa_id  TEXT NOT NULL,
  kind          TEXT NOT NULL,
  payload       BLOB NOT NULL,
  ts            INTEGER NOT NULL
) STRICT;
CREATE INDEX pending_ops_target ON pending_ops (chat_id, target_wa_id);

CREATE TABLE group_meta (
  chat_id            INTEGER PRIMARY KEY REFERENCES chats(id) ON DELETE CASCADE,
  subject            TEXT,
  topic              TEXT,
  owner_id           INTEGER REFERENCES identities(id),
  addressing_mode    TEXT,
  announce_only      INTEGER NOT NULL DEFAULT 0,
  locked             INTEGER NOT NULL DEFAULT 0,
  participant_count  INTEGER,
  fetched_at         INTEGER NOT NULL
) STRICT;

CREATE TABLE group_members (
  chat_id      INTEGER NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
  identity_id  INTEGER NOT NULL REFERENCES identities(id),
  role         INTEGER NOT NULL DEFAULT 0,
  updated_at   INTEGER NOT NULL,
  PRIMARY KEY (chat_id, identity_id)
) STRICT, WITHOUT ROWID;
CREATE INDEX group_members_identity ON group_members (identity_id);

CREATE TABLE sync_state (
  key         TEXT PRIMARY KEY,
  value       BLOB,
  updated_at  INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

-- Full-text index of textfold-folded bodies, maintained by the writer.
CREATE VIRTUAL TABLE messages_fts USING fts5 (
  body, content = '', contentless_delete = 1,
  tokenize = 'unicode61 remove_diacritics 2'
);
