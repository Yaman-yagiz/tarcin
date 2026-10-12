// SPDX-License-Identifier: GPL-3.0-or-later

package textfold

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/Yaman-yagiz/tarcin/internal/devtools/synth"
)

func TestFold(t *testing.T) {
	tests := []struct{ in, want string }{
		{"hello WORLD", "hello world"},
		{"Kadın", "kadin"},
		{"KADIN", "kadin"},
		{"kadin", "kadin"},
		{"İstanbul", "istanbul"},
		{"ISTANBUL", "istanbul"},
		{"istanbul", "istanbul"},
		{"IĞDIR", "igdir"},
		{"Iğdır", "igdir"},
		{"ŞEKER şeker", "seker seker"},
		{"Çağrı Öztürk", "cagri ozturk"},
		{"Ünlü Gölü", "unlu golu"},
		{"ışık", "isik"},
		{"Straße", "strasse"},
		{"ﬁle", "file"},
		{"café", "cafe"},
		{"E\u0301lif", "elif"},
		{"A\u200bli", "ali"},
		{"Ελλάδα", "ελλαδα"},
		{"Привет", "привет"},
		{"田中太郎", "田中太郎"},
		{"👍", "👍"},
		{"❤\ufe0f", "❤"},
		{"\uab96\u13c6\u13f8\u13f0", "\u13c6\u13c6\u13f0\u13f0"}, // Cherokee folds to uppercase
		{"\xf7\u00fd", "\ufffdy"},                                // invalid byte next to a composed letter
		{"", ""},
	}
	for _, tt := range tests {
		if got := Fold(tt.in); got != tt.want {
			t.Errorf("Fold(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestQuery(t *testing.T) {
	tests := []struct {
		query, text string
		want        bool
	}{
		{"kadin", "Kadınlar Günü", true},
		{"KADIN", "kadınlar", true},
		{"ayşe yıl", "Ayşe Yılmaz", true},
		{"ayse yil", "AYŞE YILMAZ", true},
		{"yilmaz ayse", "Ayşe Yılmaz", true}, // order does not matter
		{"ayşe kaya", "Ayşe Yılmaz", false},
		{"cagri", "Çağrı", true},
		{"istanbul", "İSTANBUL'a gidiyoruz", true},
		{"", "anything", true},
		{"   ", "anything", true},
		{"x", "", false},
	}
	for _, tt := range tests {
		q := NewQuery(tt.query)
		if got := q.Match(tt.text); got != tt.want {
			t.Errorf("NewQuery(%q).Match(%q) = %v, want %v", tt.query, tt.text, got, tt.want)
		}
		if got := q.MatchFolded(Fold(tt.text)); got != tt.want {
			t.Errorf("MatchFolded disagrees with Match for %q / %q", tt.query, tt.text)
		}
	}
	if !NewQuery("").Empty() || NewQuery("a").Empty() {
		t.Error("Empty is wrong")
	}
}

func TestASCIIFastPathDoesNotAllocate(t *testing.T) {
	s := "already lowercase ascii text"
	if n := testing.AllocsPerRun(100, func() { _ = Fold(s) }); n != 0 {
		t.Errorf("Fold allocated %v times on lowercase ASCII", n)
	}
}

func TestConcurrentUse(t *testing.T) {
	t.Parallel()
	done := make(chan struct{})
	for range 8 {
		go func() {
			defer func() { done <- struct{}{} }()
			for range 500 {
				if Fold("İSTANBUL Çağrı") != "istanbul cagri" {
					t.Error("concurrent Fold returned a wrong result")
					return
				}
			}
		}()
	}
	for range 8 {
		<-done
	}
}

func FuzzFold(f *testing.F) {
	for _, s := range []string{"Kadın", "İı", "Straße", "E\u0301", "İ\u0307", "ǅ", "ᾈ", "\xff"} {
		f.Add(s)
	}
	for _, s := range synth.Hostile() {
		if len(s) < 1<<12 {
			f.Add(s)
		}
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := Fold(s)
		if utf8.ValidString(s) && !utf8.ValidString(out) {
			t.Fatalf("Fold(%q) = %q is not valid UTF-8", s, out)
		}
		if again := Fold(out); again != out {
			t.Fatalf("Fold is not idempotent: %q -> %q -> %q", s, out, again)
		}
		if strings.ContainsAny(out, "Iıİ") {
			t.Fatalf("Fold(%q) = %q still contains a Turkish i form", s, out)
		}
		for _, r := range out {
			if unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf) {
				t.Fatalf("Fold(%q) = %q contains mark or format U+%04X", s, out, r)
			}
		}
	})
}

func BenchmarkFold(b *testing.B) {
	for name, in := range map[string]string{
		"ascii":   "Meeting Tomorrow At Ten",
		"turkish": "Çağrı akşam İstanbul'a geliyor mu? Iğdır'dan döndü.",
	} {
		b.Run(name, func(b *testing.B) {
			for b.Loop() {
				_ = Fold(in)
			}
		})
	}
}

func TestVersionNamesTheUnicodeTables(t *testing.T) {
	v := Version()
	if !strings.HasPrefix(v, "1/x-text-") || !strings.Contains(v, "/go-") {
		t.Fatalf("Version() = %q", v)
	}
}
