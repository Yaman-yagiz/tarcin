-- SPDX-License-Identifier: GPL-3.0-or-later
-- The people a message mentions, as a JSON array of their JIDs in the
-- form the message used. The body holds "@" and the user part of each.
-- JIDs, not identity IDs: reads resolve them through jid_aliases, which
-- identity merges keep current (docs/architecture/data-model.md §3).
ALTER TABLE messages ADD COLUMN mentions TEXT;
