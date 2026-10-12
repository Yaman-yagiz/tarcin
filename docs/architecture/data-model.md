# Data model and storage

- **Decisions:** [ADR-0005](../adr/0005-sqlite-storage.md),
  [ADR-0009](../adr/0009-lid-canonical-identity.md)

This document describes tarcin's storage. The schema is
created by migration `0001`. Later changes go through new migrations,
never by editing `0001`.

## 1. Files and locations

Each profile has its own directory. The MVP ships only the `default`
profile; the `--profile NAME` flag reserves room for more.

| Path | Content | Mode |
|---|---|---|
| `$XDG_DATA_HOME/tarcin/` | Root data directory | 0700 |
| `…/profiles/<name>/session.db` (+ `-wal`, `-shm`) | whatsmeow device store (keys, Signal sessions) | 0600 |
| `…/profiles/<name>/tarcin.db` (+ `-wal`, `-shm`) | Our chats, messages, identities, FTS | 0600 |
| `…/profiles/<name>/lock` | Single-instance `flock` | 0600 |
| `$XDG_CACHE_HOME/tarcin/profiles/<name>/media/` | Downloaded media, content-addressed | 0700 / 0600 |
| `$XDG_STATE_HOME/tarcin/` | Logs, compose history, UI state | 0700 / 0600 |
| `$XDG_STATE_HOME/tarcin/profiles/<name>/drafts/` | Drafts while `$EDITOR` edits them; emptied at start | 0700 / 0600 |
| `$XDG_CONFIG_HOME/tarcin/config.toml` | User configuration (never rewritten by tarcin) | user-owned |

Why two databases:

- whatsmeow's writes never contend with our bulk history inserts.
- Logging out, or resetting the session, is just deleting one file.
- whatsmeow's schema stays under whatsmeow's own migrations
  (`Container.Upgrade`), and ours under ours.

The file modes are enforced in two ways:

- The process umask is set to `0077` before any file is created.
- The modes are verified at startup. tarcin warns and offers to fix
  looser modes. Paths are resolved as described in
  [ADR-0008](../adr/0008-xdg-paths-and-file-permissions.md).

## 2. Identity model

WhatsApp addresses a person either by phone number (`…@s.whatsapp.net`,
PN) or by LID (`…@lid`). Since 2026-07, outgoing DMs always go to the
LID. Clients that keyed chats on the raw JID ended up with split chats.

**Rule:** every person is one row in `identities`.

- The **LID** is the canonical key when it is known.
- The **PN** is an attribute. It may be missing, redacted or changed.
- Every JID we see, whether PN or LID, maps to an identity row through
  `jid_aliases`.

Resolution order for an incoming JID:

1. Look the JID up in `jid_aliases`.
2. If it is not there, ask whatsmeow's LID map (`Store.LIDs`). This is a
   local lookup, never a network call.
3. If there is still no match, create a provisional identity under the
   JID as given.

**Merging:** when a PN↔LID mapping is learned, the writer merges the two
identities in **one transaction** (`internal/store/identity.go`).
Mappings can come from history sync `phoneNumberToLidMappings`, from
group participant lists, from `SenderAlt` on messages, or from
whatsmeow's map. The LID identity survives. The merge:

- repoints `messages.sender_id`, `group_members`, `reactions`,
  `group_meta.owner_id` and the aliases to the surviving row. Of two
  reactions by the person to one message the newer stays; of two
  memberships in one group the higher role stays.
- merges the two DM chats, if both exist. Copies of one message in both
  are folded together with the same rules as redeliveries, edits,
  revokes and receipts, and pending operations that waited in one chat
  for a message of the other are applied. A chat-list field set in
  either chat stays set. The unread count is the sum of both, capped by
  the incoming messages after the last read, so a message counted in
  both chats counts once.
- recomputes the chat's last activity and last message
- deletes the old identity and fills the survivor's missing names from it
- reports the removed chat and both identities to the UI

If the number's identity already has a different LID, the two are
different people: the number was recycled. It moves to the new owner and
nothing is merged.

**This account** is the identity with `is_me` set. Its JIDs come from
the session store at start, or from `AccountLinked` when a device links.
Messages sent from this account on any device are attributed to it, and
it shows as "You". `tarcin logout` may keep `tarcin.db`; if a different
account is linked to it later, `AccountLinked` clears `is_me` on the old
one, which from then on is someone else. `AccountLinked` is therefore
the one event whose order matters; there is one per link.

The writer keeps two invariants: every `lid` and `pn` in `identities`
has its alias, and a DM chat's `jid` is its peer's canonical JID. A
property test checks guarantee G2 and these invariants: random scenarios
over people who use both address forms, delivered shuffled, duplicated
and in random transaction batches, must end in the state of the
canonical delivery. `TARCIN_PROPERTY_SEEDS` runs it longer.

**Display name** is resolved at render time, never frozen at ingest
(that was whatscli's issue #124). Priority order:

1. contact name (from app state)
2. push name
3. business name
4. formatted phone number
5. shortened LID

The UI keeps an LRU of identity → display name. Identity-update events
invalidate it.

## 3. Schema (migration 0001)

The authoritative schema is
[`internal/store/migrations/0001_initial.sql`](../../internal/store/migrations/0001_initial.sql);
this section mirrors it with comments.

All tables use `STRICT`. Times are Unix **milliseconds** (`INTEGER`).
Message bodies are stored **raw**; they are sanitised at render time (see
[UI](ui.md) §7). The IDs of identities, chats and messages use
`AUTOINCREMENT` so that a deleted ID is never reused: the UI caches
display names and rendered messages by ID, and the identity merge deletes
rows.

```sql
CREATE TABLE identities (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  lid           TEXT UNIQUE,          -- canonical when known, e.g. '123@lid'
  pn            TEXT UNIQUE,          -- '905551112233@s.whatsapp.net', may be NULL
  contact_name  TEXT,
  push_name     TEXT,
  business_name TEXT,
  is_me         INTEGER NOT NULL DEFAULT 0,
  updated_at    INTEGER NOT NULL,
  CHECK (lid IS NOT NULL OR pn IS NOT NULL)
) STRICT;

CREATE TABLE jid_aliases (
  jid          TEXT PRIMARY KEY,
  identity_id  INTEGER NOT NULL REFERENCES identities(id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;

CREATE TABLE chats (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  jid               TEXT NOT NULL UNIQUE,   -- group JID, or the peer's canonical JID
  kind              TEXT NOT NULL CHECK (kind IN ('dm','group','broadcast','newsletter')),
  peer_identity_id  INTEGER REFERENCES identities(id),
  title             TEXT,                   -- group subject cache
  last_message_id   INTEGER,
  last_activity_ts  INTEGER NOT NULL DEFAULT 0,
  unread_count      INTEGER NOT NULL DEFAULT 0,
  last_read_ts      INTEGER NOT NULL DEFAULT 0,
  pinned_at         INTEGER,
  archived          INTEGER NOT NULL DEFAULT 0,
  muted_until       INTEGER,                -- -1 = forever
  ephemeral_secs    INTEGER,
  history_complete  INTEGER NOT NULL DEFAULT 0,  -- the phone has no older messages
  updated_at        INTEGER NOT NULL
) STRICT;
CREATE INDEX chats_list ON chats (archived, pinned_at DESC, last_activity_ts DESC);

CREATE TABLE messages (
  id            INTEGER PRIMARY KEY AUTOINCREMENT, -- local sort tiebreak / FTS rowid
  chat_id       INTEGER NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
  wa_id         TEXT NOT NULL,              -- WhatsApp message ID
  sender_id     INTEGER REFERENCES identities(id),
  from_me       INTEGER NOT NULL,
  ts            INTEGER NOT NULL,           -- server timestamp
  kind          TEXT NOT NULL,              -- text, image, video, audio, document, sticker,
                                            -- location, contact, poll, system, unsupported
  body          TEXT,                       -- text or caption, raw
  quoted_wa_id  TEXT,
  status        INTEGER NOT NULL,           -- 0 pending,1 sent,2 delivered,3 read,4 played,9 failed
  edited_at     INTEGER,
  revoked_at    INTEGER,
  flags         INTEGER NOT NULL DEFAULT 0, -- bitset: forwarded, view_once, mentions_me, …
  revision      INTEGER NOT NULL DEFAULT 0, -- bumped on every visible change; UI cache key
  raw           BLOB,                       -- serialized waE2E.Message for non-text kinds
  UNIQUE (chat_id, wa_id)
) STRICT;
CREATE INDEX messages_chat_ts ON messages (chat_id, ts);
CREATE INDEX messages_sender ON messages (sender_id);   -- identity merge

CREATE TABLE media (
  message_id   INTEGER PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
  mime         TEXT,
  size         INTEGER,
  file_name    TEXT,                        -- untrusted; never used as a path
  width        INTEGER,
  height       INTEGER,
  duration_ms  INTEGER,
  sha256       BLOB,
  state        INTEGER NOT NULL DEFAULT 0,  -- 0 remote,1 downloading,2 local,3 failed,4 expired
  local_name   TEXT,                        -- content-addressed name inside the cache dir
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

-- Operations (edit, revoke, receipt) whose target message has not arrived yet.
-- Not keyed on (target, kind, ts): two group members' receipts can share
-- a millisecond.
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
  addressing_mode    TEXT,                  -- 'pn' | 'lid'
  announce_only      INTEGER NOT NULL DEFAULT 0,
  locked             INTEGER NOT NULL DEFAULT 0,
  participant_count  INTEGER,
  fetched_at         INTEGER NOT NULL     -- last full member list; 0: never
) STRICT;

CREATE TABLE group_members (
  chat_id      INTEGER NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
  identity_id  INTEGER NOT NULL REFERENCES identities(id),
  role         INTEGER NOT NULL DEFAULT 0,  -- 0 member, 1 admin, 2 superadmin
  updated_at   INTEGER NOT NULL,
  PRIMARY KEY (chat_id, identity_id)
) STRICT, WITHOUT ROWID;
CREATE INDEX group_members_identity ON group_members (identity_id);  -- identity merge

CREATE TABLE sync_state (
  key         TEXT PRIMARY KEY,
  value       BLOB,
  updated_at  INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

-- Bodies folded by textfold, written by the writer.
CREATE VIRTUAL TABLE messages_fts USING fts5 (
  body, content='', contentless_delete=1,
  tokenize='unicode61 remove_diacritics 2'
);
```

Migration `0002` adds a trigger, so that deleting a message, directly or
through a cascade, also removes its index entry:

```sql
CREATE TRIGGER messages_fts_delete AFTER DELETE ON messages BEGIN
  DELETE FROM messages_fts WHERE rowid = old.id;
END;
```

Migration `0003` records which messages count as unread:

```sql
ALTER TABLE messages ADD COLUMN unread INTEGER NOT NULL DEFAULT 0;
CREATE INDEX messages_unread ON messages (chat_id, ts) WHERE unread = 1;
```

Migration `0004` records whom a message mentions, as a JSON array of
JIDs in the form the message used; the body holds `@` and the user part
of each. Reads resolve them through `jid_aliases`, which identity merges
keep current, so a mention always names the person's current identity:

```sql
ALTER TABLE messages ADD COLUMN mentions TEXT;
```

Migration `0005` records when a disappearing message disappears: its
time plus the timer it was sent with. The app deletes expired messages,
their media files included, at start and every few minutes, as the phone
does:

```sql
ALTER TABLE messages ADD COLUMN expires_at INTEGER;
CREATE INDEX messages_expiry ON messages (expires_at) WHERE expires_at IS NOT NULL;
```

Design notes:

- **Idempotency.** Messages are inserted with
  `INSERT … ON CONFLICT(chat_id, wa_id) DO UPDATE`. The update only fills
  fields that are better than the stored ones: a status can only move
  forward, and `raw` and media fields are added when missing. If the
  incoming sender differs from the stored sender, a data-quality counter
  is incremented and a warning is logged without content.
- **Order independence.** An edit, revoke or receipt for an unknown
  target goes into `pending_ops`. When the target message is inserted,
  the matching ops are applied and deleted in the same transaction.
  Pending ops older than 30 days are pruned by the writer's hourly
  upkeep. The rules are chosen so that every order of the same events
  gives the same row, which the applier tests check over all
  permutations:
  - the newest edit wins (`edited_at`);
  - a revoke wins over every edit, and clears the body, `raw`, the edit
    time and the media row;
  - receipts only move the status forward (`MessageStatus.Advance`);
  - reactions are stored on their own, keyed by target and sender, and
    joined at read time. The newest one wins (ties broken by the emoji),
    and a removal is kept as an empty-emoji tombstone, so that a late,
    older reaction cannot bring it back.
- **Who may change a message.** An edit or revoke carries its actor.
  It applies only if the actor is the message's sender, or, for a revoke
  marked as an admin's, a known admin of the group
  ([security](security.md) §4). Changes made by tarcin itself carry no
  actor and are trusted.
- **Better redeliveries.** A redelivered message can improve its row: a
  missing `raw` or media field is filled in, and a placeholder of kind
  `unsupported` (an undecryptable message) is replaced by the decrypted
  content, unless the message was revoked.
- **Names from messages** (`PushName`) only fill an empty `push_name`,
  because messages arrive out of order. `ContactUpdated` events carry the
  current names and overwrite them. tarcin's own push name is never
  taken from contacts.
- **Malformed events** (no message ID, an unknown kind of JID) are skipped
  and counted, not failed. A failed event is not acknowledged, and the
  server would redeliver it forever.
- **Runs of messages.** Consecutive messages of one submission, such as
  a history chunk, form a run. The applier defers each chat's row update
  (the last message, the activity time, the unread count) and the
  read-back of the messages for the UI to the end of the run, so that a
  chunk writes each chat once, and a chat with more messages than
  `Changes` keeps gets its reload marker without reading any of them
  back. It looks for a message's pending operations only in chats that
  have some, which it checks once per chat. Any other event, and a merge
  of two identities, ends the run first. The end state is therefore the
  same as when the events are applied one at a time, which the applier
  tests check.
- **Group receipts.** A message's status advances on the first receipt
  of each kind, so in a group "read" means read by someone. Per-member
  receipts (message info) are later work.
- **`raw`** keeps the protobuf for non-text messages, so that media can
  be downloaded later (it holds the media key) and new message kinds can
  be re-parsed without a re-sync. Plain text does not store `raw`.
- **The full-text index** holds `textfold.Fold(body)` under the
  message ID. The writer indexes a message when it is inserted,
  re-indexes it (delete, then insert) when an edit or a decrypted
  redelivery changes the body, and removes it on revoke. The index is
  stamped in `sync_state` with `textfold.Version()`, which names the
  folding rules and the Unicode tables of Go and `x/text`. When an
  upgrade changes them, the writer's upkeep rebuilds the index, so that
  stored keys and queries keep folding alike.
- **`media.file_name`** is untrusted display metadata. Files on disk are
  named by content hash (see [security](security.md) §4).
- **The unread count** is maintained by the writer. A message is
  unread (`messages.unread`) when it arrives live, is not from me, is not
  a system message, and is newer than the chat's `last_read_ts`; it then
  adds one to `unread_count`. History messages are never unread: a
  history sync sets the conversation's own count instead. A read
  (`ChatRead`, from this device or another) clears the flags up to its
  time and sets the count to the unread messages after it; an older read
  changes nothing. Because the flags are per message, reads, messages
  and identity merges give the same count in any order. A merge keeps
  each chat's synced count and recounts the flags under the later of
  the two reads.
- **The chat-list order** is pinned first, then `last_activity_ts`, then
  `id`. It survives restarts (unlike whatscli #125).

## 4. Query patterns

The queries live in `store.Reader` (`internal/store/read.go`).

| Use | Query shape | Index |
|---|---|---|
| Chat list at start-up (all chats; the UI filters archived chats and titles in memory) | `ORDER BY pinned_at DESC, last_activity_ts DESC, id DESC` | none: a sort of at most a few thousand rows |
| Chat list page, if paging is ever needed | `WHERE archived=0 ORDER BY pinned_at DESC, last_activity_ts DESC LIMIT ?` | `chats_list` |
| Changed chats or people, by ID | `WHERE id IN (SELECT value FROM json_each(?))` | primary key |
| Newest page of a chat | `WHERE chat_id=? ORDER BY ts DESC, id DESC LIMIT ?` | `messages_chat_ts` |
| Older page | `… AND (ts, id) < (?, ?) ORDER BY ts DESC, id DESC LIMIT ?` | `messages_chat_ts` |
| Newer page | `… AND (ts, id) > (?, ?) ORDER BY ts ASC, id ASC LIMIT ?` | `messages_chat_ts` |
| Search | `messages_fts MATCH ?` joined to `messages`, optionally filtered by `chat_id`, newest first | FTS5, then a sort of the matches |
| The quote of every message read (a preview of the quoted message, also outside the loaded window) | `LEFT JOIN messages q ON q.chat_id = m.chat_id AND q.wa_id = m.quoted_wa_id` | `UNIQUE (chat_id, wa_id)` |
| A message by its WhatsApp ID; the page around it, for a jump to a quote | `WHERE chat_id=? AND wa_id=?`, then an older and a newer page | `UNIQUE (chat_id, wa_id)`, `messages_chat_ts` |
| Group members (on demand) | `WHERE chat_id=? ORDER BY is_me DESC, role DESC, <unnamed last>, textfold(name) LIMIT ? OFFSET ?` | PK, then a sort of the group's members |

`TestSchemaQueryPlans` and `TestReaderQueryPlans` check the indexed
shapes against `EXPLAIN QUERY PLAN`: each must use its index and must not
sort.

Search terms are folded with `textfold` and each becomes a quoted prefix
term (`"kadin"*`), so user input can never inject FTS5 syntax. Matching
is by word prefix, while the in-memory chat-title filter matches
substrings.

**`OFFSET` is never used for message history.** The group members list
is small enough per page, and a keyset version can replace its `OFFSET`
if a benchmark shows the need.

## 5. Connections

- **Writer:** one connection (`SetMaxOpenConns(1)`) with
  `_txlock=immediate`, owned by `store.Writer`.
- **Readers:** a pool of 4 `_query_only` connections, used from
  `tea.Cmd`s and jobs.
- **Session DB:** whatsmeow gets its own `*sql.DB` through
  `sqlstore.NewWithDB(db, "sqlite3", log)` followed by
  `container.Upgrade(ctx)`.

Both databases use our registered driver `sqlite3-tarcin`. It is
mattn/go-sqlite3 built with `-tags libsqlite3` in the Arch package, and
its connect hook applies these settings to every connection (see the
SQLite [pragma](https://sqlite.org/pragma.html) and
[WAL](https://sqlite.org/wal.html) documentation):

| Setting | Value | Why |
|---|---|---|
| `journal_mode` | `WAL` | Readers and the writer run concurrently |
| `synchronous` | `NORMAL` | Safe from corruption under WAL. A power loss may roll back the last commits; an application crash loses nothing (G1 in [event pipeline](event-pipeline.md)) |
| `foreign_keys` | `ON` | Integrity; required by whatsmeow |
| `busy_timeout` | `5000` | Wait for a lock instead of failing at once with `SQLITE_BUSY` |
| `BEGIN IMMEDIATE` | writer only | A deferred transaction that upgrades from read to write can fail with `SQLITE_BUSY` without calling the busy handler |
| `temp_store` | `MEMORY` | Faster sorts and temporary tables |
| `cache_size` | `-16384` (16 MiB per connection) | Measured on the storage benchmarks |
| `journal_size_limit` | 64 MiB | Bounds the WAL file after large history syncs |
| `secure_delete` | `ON` | Deleted messages are overwritten on disk |
| `mmap_size` | off | An I/O error under mmap becomes `SIGBUS` |
| `PRAGMA optimize` | at open, hourly, at close | Writer connection only, since it may write statistics |

Rows are closed promptly, since a reader that never pauses blocks
checkpoints and lets the WAL grow. DSNs are built with `net/url`, so a
path that contains `?`, `#` or `%` cannot inject URI parameters, and
the database must never live on a network filesystem.

## 6. Migrations

- Migrations are embedded SQL files, `internal/store/migrations/NNNN_name.sql`.
- Each one is applied in a transaction that also sets
  `PRAGMA user_version`.
- **Forward-only.** If the database's version is higher than any version
  the binary knows, tarcin refuses to start with a clear message
  (downgrades are unsupported, as in whatsmeow).
- Before a migration, a `VACUUM INTO` snapshot is taken when the database
  already has a schema (`user_version` > 0). Only the newest snapshot,
  `tarcin.pre-migration-vN.db` (mode 0600), is kept in the state
  directory.
- File names must match `NNNN_name.sql`, numbered from `0001` without
  gaps; the runner refuses anything else.

## 7. Retention, deletion and backup

| Command | Behaviour |
|---|---|
| `:delete-local` | Removes a chat and its messages and media from local storage, after asking. Never touches the server. A chat deleted or cleared on another device (app state `DeleteChat`, `ClearChat`) loses its messages up to the action's time the same way; a deleted chat with nothing newer goes. Cache files go when no stored message uses them |
| `tarcin backup FILE` | A gzipped tar archive with a `VACUUM INTO` copy of `tarcin.db` and the config file. The session DB is left out unless `--with-keys` is given, since anyone with it can read the account's new messages |
| `tarcin logout` | Unlinks the device on the server, deletes `session.db`, and asks whether to keep or delete `tarcin.db` and the media cache |
| Media cache cap | LRU eviction, default 2 GiB |
