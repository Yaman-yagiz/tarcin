// SPDX-License-Identifier: GPL-3.0-or-later

// Package linkscreen is the screen that links tarcin to a WhatsApp
// account: a QR code to scan, a code to type on the phone instead, and
// the passkey hand-off that some accounts require
// (docs/architecture/ui.md §11). It shows the backend's LinkProgress
// events and asks the backend for the other ways of linking through
// commands; it performs no I/O itself.
package linkscreen

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/sanitize"
	"github.com/Yaman-yagiz/tarcin/internal/ui/clipboard"
	"github.com/Yaman-yagiz/tarcin/internal/ui/uikit"
)

// Options are the backend actions the screen can start. A nil action is
// not offered.
type Options struct {
	// PairWithPhone asks for a code to type on the phone; its command
	// returns a CodeMsg.
	PairWithPhone func(phone string) tea.Cmd
	// SubmitPasskey sends the browser's passkey response; its command
	// returns a ResultMsg.
	SubmitPasskey func(response string) tea.Cmd
	// ConfirmLink confirms the code shown during a passkey hand-off; its
	// command returns a ResultMsg.
	ConfirmLink func() tea.Cmd
	// Retry starts linking again after a failure.
	Retry func() tea.Cmd
	// Begin, if set, makes the screen open on the first-run notice
	// (account risk, data at rest); linking starts only when the user
	// accepts it. DataDir is where the notice says the data is kept.
	Begin   func() tea.Cmd
	DataDir string
	// QuitKey is the key that quits, for the notice's hint; the root
	// model sets it from the keymap.
	QuitKey string
}

// CodeMsg carries the pairing code PairWithPhone asked for.
type CodeMsg struct {
	Code string
	Err  error
}

// ResultMsg carries the outcome of SubmitPasskey or ConfirmLink.
type ResultMsg struct {
	Err error
}

type step uint8

const (
	stepWelcome step = iota
	stepConnecting
	stepQR
	stepPhoneEntry
	stepPhoneWaiting
	stepPhoneCode
	stepPasskey
	stepConfirm
	stepFailed
)

// passkeySnippet is what the user runs in the browser console on
// web.whatsapp.com. WebAuthn binds the answer to that origin, so no other
// page, and no terminal program, can produce it.
//
//nolint:gosec // JavaScript shown to the user, not a credential
const passkeySnippet = `const opts = PublicKeyCredential.parseRequestOptionsFromJSON(%s);
const cred = await navigator.credentials.get({ publicKey: opts });
console.log(JSON.stringify(cred.toJSON()));`

// Model is the link screen.
type Model struct {
	ctx  *uikit.Context
	opts Options

	step     step
	progress domain.LinkProgress
	code     string // the pairing code
	problem  string // the latest error, shown until the next step
	input    textinput.Model

	qr    []string // rendered progress.QR
	qrFor string

	width, height int
}

// New returns a link screen that waits for the first QR code.
func New(ctx *uikit.Context, opts Options) *Model {
	in := textinput.New()
	in.Prompt = "> "
	in.CharLimit = 8192
	m := &Model{ctx: ctx, opts: opts, input: in, step: stepConnecting}
	if opts.Begin != nil {
		m.step = stepWelcome
	}
	return m
}

// Welcoming reports whether the screen still shows the first-run notice.
func (m *Model) Welcoming() bool { return m.step == stepWelcome }

// Resize sets the screen size.
func (m *Model) Resize(w, h int) {
	m.width, m.height = w, h
	m.input.SetWidth(max(w-4, 10))
}

// Progress applies a LinkProgress event from the backend. A new QR code
// does not interrupt a user who chose another way of linking.
func (m *Model) Progress(p domain.LinkProgress) {
	m.progress = p
	if m.step == stepWelcome {
		return // linking has not started; the notice stays until accepted
	}
	switch p.State {
	case domain.LinkQR:
		if m.step == stepConnecting || m.step == stepQR {
			m.step = stepQR
		}
	case domain.LinkPasskey:
		m.step, m.problem = stepPasskey, ""
		m.focusInput("Paste the line the console printed")
	case domain.LinkConfirm:
		m.step, m.problem = stepConfirm, ""
		m.input.Blur()
	case domain.LinkFailed:
		m.step = stepFailed
		m.input.Blur()
	}
}

func (m *Model) focusInput(placeholder string) {
	m.input.Reset()
	m.input.Placeholder = placeholder
	m.input.Focus()
}

// Update handles keys and the results of the screen's commands.
func (m *Model) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case CodeMsg:
		if m.step != stepPhoneWaiting {
			return nil
		}
		if msg.Err != nil {
			m.step, m.problem = stepPhoneEntry, msg.Err.Error()
			m.focusInput("+90 555 123 45 67")
			return nil
		}
		m.step, m.code, m.problem = stepPhoneCode, msg.Code, ""
		return nil
	case ResultMsg:
		if msg.Err != nil {
			m.problem = msg.Err.Error()
		}
		return nil
	case tea.KeyPressMsg:
		return m.key(msg)
	case tea.PasteMsg:
		if m.input.Focused() {
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return cmd
		}
	}
	return nil
}

func (m *Model) key(k tea.KeyPressMsg) tea.Cmd {
	key := k.String()
	switch m.step {
	case stepWelcome:
		if key == "enter" {
			m.step = stepConnecting
			return m.opts.Begin()
		}
		return nil
	case stepQR:
		if key == "p" && m.opts.PairWithPhone != nil {
			m.step, m.problem = stepPhoneEntry, ""
			m.focusInput("+90 555 123 45 67")
		}
		return nil
	case stepPhoneEntry:
		switch key {
		case "esc":
			m.backToQR()
			return nil
		case "enter":
			phone := strings.TrimSpace(m.input.Value())
			if phone == "" {
				return nil
			}
			m.step, m.problem = stepPhoneWaiting, ""
			m.input.Blur()
			return m.opts.PairWithPhone(phone)
		}
	case stepPhoneCode, stepPhoneWaiting:
		if key == "esc" {
			m.backToQR()
		}
		return nil
	case stepPasskey:
		switch key {
		case "ctrl+y":
			return clipboard.Copy(m.snippet())
		case "enter":
			resp := strings.TrimSpace(m.input.Value())
			if resp == "" || m.opts.SubmitPasskey == nil {
				return nil
			}
			m.problem = ""
			return m.opts.SubmitPasskey(resp)
		}
	case stepConfirm:
		switch key {
		case "y":
			if m.opts.ConfirmLink != nil {
				return m.opts.ConfirmLink()
			}
		case "n":
			m.Progress(domain.LinkProgress{State: domain.LinkFailed, Reason: "The codes did not match, so the device was not linked"})
		}
		return nil
	case stepFailed:
		if key == "enter" && m.opts.Retry != nil {
			m.step, m.problem, m.code = stepConnecting, "", ""
			return m.opts.Retry()
		}
		return nil
	}
	if m.input.Focused() {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(k)
		return cmd
	}
	return nil
}

func (m *Model) backToQR() {
	m.step, m.problem, m.code = stepQR, "", ""
	m.input.Blur()
}

func (m *Model) snippet() string {
	return strings.Replace(passkeySnippet, "%s", m.progress.Challenge, 1)
}

// qrStyle draws the code dark on light whatever the terminal's colors:
// many scanners do not read an inverted code.
var qrStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#000000")).Background(lipgloss.Color("#FFFFFF"))

// View returns the screen's lines; the caller fits them to the screen.
func (m *Model) View() []string {
	t := m.ctx.Theme
	var lines []string
	add := func(s ...string) { lines = append(lines, s...) }
	wrap := func(style lipgloss.Style, s string) {
		for _, l := range strings.Split(ansi.Wordwrap(s, max(m.width-4, 20), " "), "\n") {
			add("  " + style.Render(l))
		}
	}
	plain := lipgloss.NewStyle()
	add("", "  "+t.Accent.Render("Link tarcin to your WhatsApp account"), "")
	switch m.step {
	case stepWelcome:
		m.viewWelcome(add, wrap)
	case stepConnecting:
		wrap(t.Muted, "Connecting to WhatsApp…")
	case stepQR:
		m.viewQR(add, wrap)
	case stepPhoneEntry, stepPhoneWaiting:
		wrap(plain, "Your phone number, in international format with the country code:")
		add("", "  "+m.input.View(), "")
		if m.step == stepPhoneWaiting {
			wrap(t.Muted, "Asking WhatsApp for a code…")
		}
		wrap(t.Muted, "Enter  get a code    Esc  back to the QR code")
	case stepPhoneCode:
		wrap(plain, "On your phone, open WhatsApp → Settings → Linked devices → Link a device → Link with phone number instead, and type this code:")
		add("", "    "+t.Accent.Render(sanitize.Line(m.code)), "")
		wrap(t.Muted, "Esc  back to the QR code")
	case stepPasskey:
		wrap(plain, "This account protects new devices with a passkey. Only your own passkey, in a browser on web.whatsapp.com, can approve the link:")
		add("")
		wrap(plain, "1. Open https://web.whatsapp.com in a browser where you can use this account's passkey.")
		wrap(plain, "2. Open the browser console (F12) and run:")
		for _, l := range strings.Split(m.snippet(), "\n") {
			wrap(t.Muted, sanitize.Line(l))
		}
		wrap(plain, "3. Approve the passkey prompt, then paste the line the console printed:")
		add("", "  "+m.input.View(), "")
		wrap(t.Muted, "Enter  send    ctrl+y  copy the snippet")
	case stepConfirm:
		wrap(plain, "Check that your phone shows this code:")
		add("", "    "+t.Accent.Render(sanitize.Line(m.progress.Code)), "")
		wrap(t.Muted, "y  it matches    n  it does not")
	case stepFailed:
		wrap(t.Warning, sanitize.Line(m.progress.Reason))
		add("")
		if m.opts.Retry != nil {
			wrap(t.Muted, "Enter  try again")
		}
	}
	if m.problem != "" {
		add("")
		wrap(t.Warning, sanitize.Line(m.problem))
	}
	if m.step == stepQR && len(m.qr) > 0 && !m.fits(lines) {
		return m.viewQRCompact()
	}
	return lines
}

// fits reports whether lines fit the screen.
func (m *Model) fits(lines []string) bool {
	if len(lines) > m.height {
		return false
	}
	for _, l := range lines {
		if ansi.StringWidth(l) > m.width {
			return false
		}
	}
	return true
}

// viewQRCompact shows only the QR code and one line of help, for a
// terminal too small for the whole screen; if even that does not fit, it
// says how large the terminal must be and what else works.
func (m *Model) viewQRCompact() []string {
	t := m.ctx.Theme
	hint := "Scan with WhatsApp → Linked devices"
	if m.opts.PairWithPhone != nil {
		hint += " · p  phone number instead"
	}
	lines := make([]string, 0, len(m.qr)+1)
	for _, l := range m.qr {
		lines = append(lines, qrStyle.Render(l))
	}
	lines = append(lines, t.Muted.Render(ansi.Truncate(hint, m.width, "…")))
	if m.fits(lines) {
		return lines
	}
	w, h := ansi.StringWidth(m.qr[0]), len(m.qr)+1
	msg := fmt.Sprintf("The QR code needs a terminal of at least %d×%d; this one is %d×%d. "+
		"Make the window larger or the font smaller (ctrl+minus in most terminals)", w, h+1, m.width, m.height+1)
	if m.opts.PairWithPhone != nil {
		msg += ", or press p to link with your phone number instead"
	}
	var out []string
	for _, l := range strings.Split(ansi.Wordwrap(msg+".", max(m.width-2, 10), " "), "\n") {
		out = append(out, " "+t.Warning.Render(l))
	}
	return out
}

// viewWelcome is the first-run notice (docs/architecture/security.md §7).
func (m *Model) viewWelcome(add func(...string), wrap func(lipgloss.Style, string)) {
	t := m.ctx.Theme
	plain := lipgloss.NewStyle()
	wrap(t.Warning, "Before you link: tarcin is an unofficial client.")
	add("")
	wrap(plain, "WhatsApp's terms do not allow third-party clients. Using tarcin can get your account restricted or banned, even if you do nothing wrong. Never use it for bulk or automated messages. You use it at your own risk.")
	add("")
	dir := m.opts.DataDir
	if dir == "" {
		dir = "your data directory"
	}
	wrap(plain, "tarcin keeps your messages and this device's keys unencrypted in "+sanitize.Line(dir)+
		". Anyone who can read your files can read them and act as this device. Use full-disk encryption, and keep backups of that directory private.")
	add("")
	wrap(plain, "tarcin is not affiliated with WhatsApp or Meta.")
	add("")
	hint := "Enter  I understand: link this device"
	if q := m.opts.QuitKey; q != "" {
		hint += "    " + q + "  quit"
	}
	wrap(t.Muted, hint)
}

func (m *Model) viewQR(add func(...string), wrap func(lipgloss.Style, string)) {
	t := m.ctx.Theme
	plain := lipgloss.NewStyle()
	wrap(plain, "On your phone, open WhatsApp → Settings → Linked devices → Link a device, and point the camera at this code.")
	add("")
	if m.qrFor != m.progress.QR {
		m.qr, m.qrFor = nil, m.progress.QR
		if lines, err := uikit.QR(m.progress.QR); err == nil {
			m.qr = lines
		}
	}
	// View falls back to viewQRCompact if this does not fit.
	for _, l := range m.qr {
		add("  " + qrStyle.Render(l))
	}
	if len(m.qr) == 0 && m.progress.QR != "" {
		wrap(t.Warning, "The QR code could not be drawn; press p to link with your phone number instead.")
	}
	add("")
	if m.progress.Reason != "" {
		wrap(t.Warning, sanitize.Line(m.progress.Reason))
	}
	if m.opts.PairWithPhone != nil {
		wrap(t.Muted, "p  link with a phone number instead")
	}
}
