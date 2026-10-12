// SPDX-License-Identifier: GPL-3.0-or-later

package uikit

import (
	"strings"
	"testing"
	"unicode/utf8"

	"rsc.io/qr"
)

func TestQRMatchesTheCode(t *testing.T) {
	payload := "2@" + strings.Repeat("Ab3+/", 40) + ",noise,identity,secret" // the length of a real link code
	lines, err := QR(payload)
	if err != nil {
		t.Fatal(err)
	}
	code, _ := qr.Encode(payload, qr.L)
	n := code.Size + 2*qrQuiet
	if len(lines) != (n+1)/2 {
		t.Fatalf("%d lines for %d modules", len(lines), n)
	}
	for y, line := range lines {
		if utf8.RuneCountInString(line) != n {
			t.Fatalf("line %d is %d cells wide, want %d", y, utf8.RuneCountInString(line), n)
		}
		for x, r := range []rune(line) {
			top := code.Black(x-qrQuiet, 2*y-qrQuiet)
			bottom := code.Black(x-qrQuiet, 2*y+1-qrQuiet)
			want := map[[2]bool]rune{{true, true}: '█', {true, false}: '▀', {false, true}: '▄', {false, false}: ' '}[[2]bool{top, bottom}]
			if r != want {
				t.Fatalf("cell %d,%d = %q, want %q", x, y, r, want)
			}
		}
	}
	// The quiet zone is light all around.
	if strings.TrimSpace(lines[0]) != "" || strings.TrimSpace(lines[len(lines)-1]) != "" {
		t.Error("the first and last rows belong to the quiet zone")
	}
	for _, line := range lines {
		if r := []rune(line); r[0] != ' ' || r[len(r)-1] != ' ' {
			t.Fatal("the first and last columns belong to the quiet zone")
		}
	}
}

func TestQRRefusesOversizedPayloads(t *testing.T) {
	if _, err := QR(strings.Repeat("x", 5000)); err == nil {
		t.Fatal("a payload beyond the largest QR version must fail")
	}
}
