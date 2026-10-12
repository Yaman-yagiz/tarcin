-- SPDX-License-Identifier: GPL-3.0-or-later
-- Which messages count as unread. A live incoming message newer than the
-- chat's last read is unread; a read clears the flags up to its time.
-- With the flag, reads and merges keep the unread count exact whatever
-- order the events arrive in (docs/architecture/data-model.md §3).
ALTER TABLE messages ADD COLUMN unread INTEGER NOT NULL DEFAULT 0;
CREATE INDEX messages_unread ON messages (chat_id, ts) WHERE unread = 1;
