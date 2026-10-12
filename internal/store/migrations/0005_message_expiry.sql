-- SPDX-License-Identifier: GPL-3.0-or-later
-- When a disappearing message disappears: its time plus the timer it was
-- sent with. Expired messages are deleted here as on the phone
-- (docs/architecture/data-model.md §3).
ALTER TABLE messages ADD COLUMN expires_at INTEGER;
CREATE INDEX messages_expiry ON messages (expires_at) WHERE expires_at IS NOT NULL;
