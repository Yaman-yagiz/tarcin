-- SPDX-License-Identifier: GPL-3.0-or-later
-- Deleting a message, directly or through a cascade, removes it from the
-- full-text index. Inserts and edits are indexed by the writer, which
-- folds the text in Go.
CREATE TRIGGER messages_fts_delete AFTER DELETE ON messages BEGIN
  DELETE FROM messages_fts WHERE rowid = old.id;
END;
