-- SPDX-License-Identifier: GPL-3.0-or-later
-- Who has received, read or played this account's group messages: a
-- group message is delivered, read or played once every member is, as
-- the phone shows it (docs/architecture/data-model.md §3).
CREATE TABLE receipts (
  message_id   INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  identity_id  INTEGER NOT NULL REFERENCES identities(id) ON DELETE CASCADE,
  status       INTEGER NOT NULL,
  PRIMARY KEY (message_id, identity_id)
) STRICT, WITHOUT ROWID;
CREATE INDEX receipts_identity ON receipts (identity_id);
