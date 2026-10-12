// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/textfold"
)

// readFixture returns a migrated database's writer connection and a
// Reader on its read pool.
func readFixture(t testing.TB) (*sql.DB, *Reader) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tarcin.db")
	w, err := Open(t.Context(), path, RoleWriter)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	if _, _, err := Migrate(t.Context(), w, Migrations(), MigrateOptions{}); err != nil {
		t.Fatal(err)
	}
	r, err := Open(t.Context(), path, RoleReader)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return w, NewReader(r)
}

func mustExecArgs(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func TestReaderChatsOrderAndFields(t *testing.T) {
	w, r := readFixture(t)
	mustExec(t, w, "INSERT INTO identities (id, lid, pn, push_name, updated_at) VALUES (1, '1@lid', '90555@s.whatsapp.net', 'Ali', 0)")
	for _, q := range []string{
		// id, jid, kind, peer, title, last_activity, pinned_at, archived, muted, ephemeral
		"INSERT INTO chats (id, jid, kind, peer_identity_id, last_activity_ts, unread_count, muted_until, ephemeral_secs, updated_at) VALUES (1, '1@lid', 'dm', 1, 5000, 3, -1, 86400, 0)",
		"INSERT INTO chats (id, jid, kind, title, last_activity_ts, pinned_at, updated_at) VALUES (2, 'a@g.us', 'group', 'Aile', 1000, 200, 0)",
		"INSERT INTO chats (id, jid, kind, title, last_activity_ts, pinned_at, updated_at) VALUES (3, 'b@g.us', 'group', 'İş', 9000, 100, 0)",
		"INSERT INTO chats (id, jid, kind, last_activity_ts, archived, updated_at) VALUES (4, 'c@broadcast', 'broadcast', 7000, 1, 0)",
		"INSERT INTO chats (id, jid, kind, last_activity_ts, muted_until, updated_at) VALUES (5, 'd@newsletter', 'newsletter', 5000, 1700000000000, 0)",
		"INSERT INTO chats (id, jid, kind, updated_at, history_complete) VALUES (6, 'e@g.us', 'group', 0, 1)",
	} {
		mustExec(t, w, q)
	}
	chats, err := r.Chats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var ids []domain.ChatID
	for _, c := range chats {
		ids = append(ids, c.ID)
	}
	if want := []domain.ChatID{2, 3, 4, 5, 1, 6}; !slices.Equal(ids, want) {
		t.Fatalf("order %v, want %v", ids, want)
	}
	if !slices.IsSortedFunc(chats, func(a, b domain.Chat) int {
		if a.ListBefore(&b) {
			return -1
		}
		return 1
	}) {
		t.Fatal("the SQL order must agree with domain.Chat.ListBefore")
	}
	dm := chats[4]
	if dm.Kind != domain.ChatDirect || dm.PeerID != 1 || dm.Title != "" || dm.UnreadCount != 3 ||
		!dm.MutedUntil.Equal(domain.MuteForever) || dm.EphemeralTimer != 24*time.Hour || dm.IsPinned() ||
		!dm.LastActivity.Equal(time.UnixMilli(5000)) || !dm.LastReadAt.IsZero() {
		t.Fatalf("direct chat = %+v", dm)
	}
	if c := chats[0]; c.Kind != domain.ChatGroup || c.Title != "Aile" || !c.PinnedAt.Equal(time.UnixMilli(200)) {
		t.Fatalf("group = %+v", c)
	}
	if c := chats[2]; c.Kind != domain.ChatBroadcast || !c.Archived {
		t.Fatalf("broadcast = %+v", c)
	}
	if c := chats[3]; c.Kind != domain.ChatNewsletter || !c.MutedUntil.Equal(time.UnixMilli(1700000000000)) {
		t.Fatalf("newsletter = %+v", c)
	}
	if c := chats[5]; !c.LastActivity.IsZero() || !c.HistoryComplete || !c.MutedUntil.IsZero() {
		t.Fatalf("empty chat = %+v", c)
	}

	some, err := r.ChatsByID(t.Context(), []domain.ChatID{6, 99, 3})
	if err != nil || len(some) != 2 || some[0].ID != 3 || some[1].ID != 6 {
		t.Fatalf("ChatsByID = %+v, %v", some, err)
	}
	if none, err := r.ChatsByID(t.Context(), nil); none != nil || err != nil {
		t.Fatal("ChatsByID(nil) must return nothing")
	}
}

func TestReaderIdentities(t *testing.T) {
	w, r := readFixture(t)
	mustExec(t, w, `INSERT INTO identities (id, lid, pn, contact_name, push_name, business_name, is_me, updated_at) VALUES
		(1, '1@lid', '90555@s.whatsapp.net', 'Ali Veli', 'ali', 'Ali Ltd', 1, 0),
		(2, NULL, '90556@s.whatsapp.net', NULL, NULL, NULL, 0, 0),
		(3, '3@lid', NULL, NULL, 'Ayşe', NULL, 0, 0)`)
	all, err := r.Identities(t.Context())
	if err != nil || len(all) != 3 {
		t.Fatalf("Identities = %v, %v", all, err)
	}
	want := domain.Identity{ID: 1, LID: "1@lid", PN: "90555@s.whatsapp.net", ContactName: "Ali Veli",
		PushName: "ali", BusinessName: "Ali Ltd", IsMe: true}
	if all[0] != want {
		t.Fatalf("identity = %+v, want %+v", all[0], want)
	}
	if all[1].LID != "" || all[1].DisplayName() != "+90556" || all[2].DisplayName() != "~Ayşe" {
		t.Fatalf("identities = %+v", all)
	}
	some, err := r.IdentitiesByID(t.Context(), []domain.IdentityID{3, 7})
	if err != nil || len(some) != 1 || some[0].ID != 3 {
		t.Fatalf("IdentitiesByID = %+v, %v", some, err)
	}
}

func TestReaderContacts(t *testing.T) {
	w, r := readFixture(t)
	mustExec(t, w, `INSERT INTO identities (id, lid, pn, contact_name, push_name, is_me, updated_at) VALUES
		(1, '1@lid', NULL, 'Ayşe Kaya', NULL, 0, 0),
		(2, '2@lid', NULL, 'Elif Ayşegül', NULL, 0, 0),
		(3, '3@lid', NULL, 'AYSE Demir', NULL, 0, 0),
		(4, '4@lid', NULL, NULL, 'Ayşe Push', 0, 0),
		(5, '5@lid', NULL, 'Ayşe Me', NULL, 1, 0),
		(6, '6@lid', NULL, 'Mert', NULL, 0, 0)`)
	names := func(query string, limit int) []string {
		t.Helper()
		got, err := r.Contacts(t.Context(), query, limit)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, id := range got {
			out = append(out, id.ContactName)
		}
		return out
	}
	// Matching ignores case and accents; names that start with the query
	// come first. Push names and this account are not contacts.
	if got := names("ayse", 10); !slices.Equal(got, []string{"AYSE Demir", "Ayşe Kaya", "Elif Ayşegül"}) {
		t.Fatalf("contacts = %q", got)
	}
	if got := names("ayse", 1); !slices.Equal(got, []string{"AYSE Demir"}) {
		t.Fatalf("limit 1 = %q", got)
	}
	if got := names("zz", 10); len(got) != 0 {
		t.Fatalf("no match = %q", got)
	}
	if got := names("", 10); len(got) != 4 {
		t.Fatalf("an empty query lists every contact: %q", got)
	}
}

// pageFixture inserts n messages into chat 1, three per timestamp so
// that pages must break ties by ID, and noise into chat 2.
func pageFixture(t *testing.T, w *sql.DB, n int) {
	t.Helper()
	mustExec(t, w, "INSERT INTO chats (id, jid, kind, updated_at) VALUES (1, 'a@g.us', 'group', 0), (2, 'b@g.us', 'group', 0)")
	tx, err := w.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range n {
		// IDs ascend while timestamps go back and forth inside a second,
		// as when history arrives after live messages.
		ts := int64(1_000_000 + (i/3)*1000)
		if _, err := tx.ExecContext(t.Context(), `INSERT INTO messages (chat_id, wa_id, from_me, ts, kind, body, status)
			VALUES (1, ?, 0, ?, 'text', ?, 1), (2, ?, 0, ?, 'text', 'noise', 1)`,
			fmt.Sprintf("A%d", i), ts, fmt.Sprintf("m%d", i), fmt.Sprintf("B%d", i), ts); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func bodies(msgs []domain.Message) []string {
	out := make([]string, len(msgs))
	for i := range msgs {
		out[i] = msgs[i].Body
	}
	return out
}

func TestReaderPagesWalkWholeHistory(t *testing.T) {
	w, r := readFixture(t)
	const n = 250
	pageFixture(t, w, n)
	var want []string
	for i := range n {
		want = append(want, fmt.Sprintf("m%d", i))
	}

	// Backwards from the newest page, as when scrolling up.
	page, err := r.Latest(t.Context(), 1, 100)
	if err != nil || len(page) != 100 {
		t.Fatalf("Latest: %d messages, %v", len(page), err)
	}
	got := bodies(page)
	for {
		older, err := r.Before(t.Context(), 1, page[0].Cursor(), 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(older) == 0 {
			break
		}
		got = append(bodies(older), got...)
		page = older
	}
	if !slices.Equal(got, want) {
		t.Fatalf("walking back: %d messages, want %d, in order without gaps", len(got), len(want))
	}

	// Forwards from the oldest message, as when jumping to a search hit.
	last := page[0]
	if last.Body != "m0" {
		t.Fatalf("the walk back ended at %s, want m0", last.Body)
	}
	got = []string{last.Body}
	for {
		newer, err := r.After(t.Context(), 1, last.Cursor(), 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(newer) == 0 {
			break
		}
		got = append(got, bodies(newer)...)
		last = newer[len(newer)-1]
	}
	if !slices.Equal(got, want) {
		t.Fatalf("walking forward: %d messages, want %d", len(got), len(want))
	}
}

func TestReaderMessageFields(t *testing.T) {
	w, r := readFixture(t)
	mustExec(t, w, "INSERT INTO identities (id, lid, updated_at) VALUES (5, '5@lid', 0)")
	mustExec(t, w, "INSERT INTO chats (id, jid, kind, updated_at) VALUES (1, 'a@g.us', 'group', 0)")
	mustExec(t, w, `INSERT INTO messages (id, chat_id, wa_id, sender_id, from_me, ts, kind, body, quoted_wa_id,
		status, edited_at, revoked_at, flags, revision) VALUES
		(10, 1, 'W1', 5, 0, 1000, 'image', 'caption', 'Q1', 3, 2000, NULL, 5, 7),
		(11, 1, 'W2', NULL, 1, 3000, 'hologram', NULL, NULL, 9, NULL, 4000, 0, 0)`)
	mustExec(t, w, `INSERT INTO media (message_id, mime, size, file_name, width, height, duration_ms, state, updated_at)
		VALUES (10, 'image/jpeg', 12345, 'cat.jpg', 640, 480, NULL, 2, 0)`)
	msgs, err := r.Latest(t.Context(), 1, 10)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("Latest = %+v, %v", msgs, err)
	}
	img, other := msgs[0], msgs[1]
	if img.ID != 10 || img.ChatID != 1 || img.WAID != "W1" || img.SenderID != 5 || img.FromMe ||
		!img.Timestamp.Equal(time.UnixMilli(1000)) || img.Kind != domain.KindImage || img.Body != "caption" ||
		img.QuotedWAID != "Q1" || img.Status != domain.StatusRead || !img.EditedAt.Equal(time.UnixMilli(2000)) ||
		img.IsRevoked() || img.Flags != domain.FlagForwarded|domain.FlagMentionsMe || img.Revision != 7 {
		t.Fatalf("image message = %+v", img)
	}
	wantMedia := domain.Media{MIME: "image/jpeg", Size: 12345, FileName: "cat.jpg", Width: 640, Height: 480, State: domain.MediaLocal}
	if img.Media == nil || *img.Media != wantMedia {
		t.Fatalf("media = %+v", img.Media)
	}
	if other.SenderID != 0 || !other.FromMe || other.Kind != domain.KindUnsupported || other.Status != domain.StatusFailed ||
		!other.RevokedAt.Equal(time.UnixMilli(4000)) || other.Media != nil || !other.EditedAt.IsZero() {
		t.Fatalf("second message = %+v", other)
	}
	if img.Quote != nil || other.Quote != nil {
		t.Fatal("a quote that is not stored has no preview")
	}
	// Once the quoted message is stored, replies carry its preview.
	mustExec(t, w, `INSERT INTO messages (id, chat_id, wa_id, sender_id, from_me, ts, kind, body, status, flags, revision)
		VALUES (9, 1, 'Q1', 5, 0, 500, 'text', 'the question', 0, 0, 0)`)
	msgs, _ = r.Latest(t.Context(), 1, 10)
	if q := msgs[1].Quote; q == nil || q.SenderID != 5 || q.FromMe || q.Kind != domain.KindText || q.Body != "the question" || q.Revoked {
		t.Fatalf("quote = %+v", q)
	}
	if m, ok, err := r.MessageByWAID(t.Context(), 1, "W1"); err != nil || !ok || m.ID != 10 || m.Quote == nil {
		t.Fatalf("MessageByWAID = %+v, %v, %v", m, ok, err)
	}
	if _, ok, _ := r.MessageByWAID(t.Context(), 1, "nope"); ok {
		t.Fatal("an unknown ID is not found")
	}
}

func TestReaderAround(t *testing.T) {
	w, r := readFixture(t)
	mustExec(t, w, "INSERT INTO chats (id, jid, kind, updated_at) VALUES (1, 'a@g.us', 'group', 0)")
	for i := range 20 {
		mustExec(t, w, fmt.Sprintf(`INSERT INTO messages (chat_id, wa_id, from_me, ts, kind, body, status, flags, revision)
			VALUES (1, 'W%02d', 1, %d, 'text', 'x', 0, 0, 0)`, i, 1000*(i+1)))
	}
	msgs, err := r.Around(t.Context(), 1, "W10", 7)
	if err != nil || len(msgs) != 7 || msgs[0].WAID != "W07" || msgs[3].WAID != "W10" || msgs[6].WAID != "W13" {
		t.Fatalf("Around = %v, %v", waids(msgs), err)
	}
	if msgs, _ := r.Around(t.Context(), 1, "W01", 7); len(msgs) != 5 || msgs[1].WAID != "W01" {
		t.Fatalf("near the start: %v", waids(msgs))
	}
	if msgs, err := r.Around(t.Context(), 1, "nope", 7); err != nil || msgs != nil {
		t.Fatalf("unknown: %v, %v", msgs, err)
	}
}

func waids(msgs []domain.Message) []domain.WAMessageID {
	out := make([]domain.WAMessageID, len(msgs))
	for i := range msgs {
		out[i] = msgs[i].WAID
	}
	return out
}

func TestReaderSearch(t *testing.T) {
	w, r := readFixture(t)
	mustExec(t, w, "INSERT INTO chats (id, jid, kind, updated_at) VALUES (1, 'a@g.us', 'group', 0), (2, 'b@g.us', 'group', 0)")
	for i, m := range []struct {
		chat int
		body string
	}{
		{1, "Kadınlar matinesi yarın"},
		{1, "İSTANBUL'a gidiyorum"},
		{2, "Çağrı'yı aradın mı?"},
		{2, "kadin ve erkek"},
		{1, "Şeker bayramı"},
	} {
		id := i + 1
		mustExecArgs(t, w, `INSERT INTO messages (id, chat_id, wa_id, from_me, ts, kind, body, status)
			VALUES (?, ?, ?, 0, ?, 'text', ?, 1)`, id, m.chat, fmt.Sprintf("W%d", id), id*1000, m.body)
		mustExecArgs(t, w, "INSERT INTO messages_fts (rowid, body) VALUES (?, ?)", id, textfold.Fold(m.body))
	}
	for _, c := range []struct {
		query string
		chat  domain.ChatID
		want  []string
	}{
		{"kadin", 0, []string{"kadin ve erkek", "Kadınlar matinesi yarın"}},
		{"KADIN", 1, []string{"Kadınlar matinesi yarın"}},
		{"istanbul", 0, []string{"İSTANBUL'a gidiyorum"}},
		{"cagri aradin", 0, []string{"Çağrı'yı aradın mı?"}},
		{"seker bay", 0, []string{"Şeker bayramı"}},
		{"adin", 0, nil}, // a prefix of a word, not a substring
		{"kadin bayram", 0, nil},
		{"!!! ?", 0, nil},
		{"", 0, nil},
	} {
		got, err := r.Search(t.Context(), c.query, c.chat, 50)
		if err != nil {
			t.Fatalf("Search(%q): %v", c.query, err)
		}
		if !slices.Equal(bodies(got), c.want) {
			t.Errorf("Search(%q, chat %d) = %q, want %q", c.query, c.chat, bodies(got), c.want)
		}
	}
	// FTS5 syntax in a query is searched for literally, never parsed.
	for _, q := range []string{`kadin" OR "seker`, `NEAR(kadin seker)`, `kadin*`, `-kadin`, `"`, `body:kadin`, `^kadin`, `kadin AND`} {
		if _, err := r.Search(t.Context(), q, 0, 50); err != nil {
			t.Errorf("Search(%q): %v", q, err)
		}
	}
}

func TestFTSMatch(t *testing.T) {
	for in, want := range map[string]string{
		"Kadın  İSTANBUL": `"kadin"* "istanbul"*`,
		`say "hi"`:        `"say"* """hi"""*`,
		"!! ?? --":        "",
		"NEAR(a":          `"near(a"*`,
	} {
		if got := ftsMatch(in); got != want {
			t.Errorf("ftsMatch(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestReaderQueryPlans(t *testing.T) {
	w, _ := readFixture(t)
	for _, q := range []struct{ query, want string }{
		{"SELECT " + messageColumns + " FROM " + messageTables + " WHERE m.chat_id = 1 ORDER BY m.ts DESC, m.id DESC LIMIT 100", "messages_chat_ts"},
		{"SELECT " + messageColumns + " FROM " + messageTables + " WHERE m.chat_id = 1 AND (m.ts, m.id) < (5, 5) ORDER BY m.ts DESC, m.id DESC LIMIT 100", "messages_chat_ts"},
		{"SELECT " + messageColumns + " FROM " + messageTables + " WHERE m.chat_id = 1 AND (m.ts, m.id) > (5, 5) ORDER BY m.ts ASC, m.id ASC LIMIT 100", "messages_chat_ts"},
		{"SELECT " + chatColumns + " FROM chats WHERE id IN (SELECT value FROM json_each('[1,2]'))", "INTEGER PRIMARY KEY"},
		// The quote of a reply is found through the (chat_id, wa_id) key.
		{"SELECT " + messageColumns + " FROM " + messageTables + " WHERE m.chat_id = 1 ORDER BY m.ts DESC, m.id DESC LIMIT 100", "SEARCH q USING INDEX sqlite_autoindex_messages_1"},
		{"SELECT " + messageColumns + " FROM " + messageTables + " WHERE m.chat_id = 1 AND m.wa_id = 'W'", "SEARCH m USING INDEX sqlite_autoindex_messages_1"},
	} {
		rows, err := w.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+q.query)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		_ = rows.Close()
		p := strings.Join(plan, "; ")
		// The reactions subquery sorts one message's few reactions; the
		// outer query must neither sort nor scan.
		outer, sub, _ := strings.Cut(p, "CORRELATED SCALAR SUBQUERY")
		if !strings.Contains(p, q.want) || strings.Contains(outer, "TEMP B-TREE") || strings.Contains(p, "SCAN m") {
			t.Errorf("%s\n  plan: %s", q.query, p)
		}
		if strings.Contains(q.query, "FROM reactions") && !strings.Contains(sub, "SEARCH r USING PRIMARY KEY") {
			t.Errorf("the reactions of a message must be found by key:\n  plan: %s", p)
		}
	}
}
