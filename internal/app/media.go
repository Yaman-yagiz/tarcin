// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // image sizes for attachments
	_ "image/jpeg" //
	_ "image/png"  //
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Yaman-yagiz/tarcin/internal/backend"
	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/jobs"
	"github.com/Yaman-yagiz/tarcin/internal/sanitize"
	"github.com/Yaman-yagiz/tarcin/internal/store"
	"github.com/Yaman-yagiz/tarcin/internal/ui"
)

// Media (docs/architecture/security.md §4): downloads are refused beyond
// a size limit, checked against the declared size and while streaming;
// the cache names files by their content's SHA-256 and an extension from
// an allow-list, never by the sender's file name; a file is written to a
// temporary file, synced and renamed into place; the cache keeps to a
// size, dropping the least recently used files; openers run without a
// shell; and saving never replaces a file.

// mediaOptions configure media handling.
type mediaOptions struct {
	Dir         string // the media cache
	Limit       int64  // the largest download, in bytes
	UploadLimit int64  // the largest attachment sent, in bytes
	Cap         int64  // the cache's size, in bytes
	SaveDir     string // where media is saved
	Open        []string
	Openers     map[string][]string // by MIME type, "image/*" or exact
	// start runs an opener without waiting for it; tests replace it.
	start func(argv []string) error
}

// extensions maps the MIME types tarcin names cache files after; any
// other is ".bin". A sender's file name never chooses it.
var extensions = map[string]string{
	"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "image/gif": ".gif", "image/heic": ".heic",
	"video/mp4": ".mp4", "video/3gpp": ".3gp", "video/quicktime": ".mov", "video/webm": ".webm",
	"audio/ogg": ".ogg", "audio/mpeg": ".mp3", "audio/mp4": ".m4a", "audio/aac": ".aac", "audio/amr": ".amr",
	"audio/wav": ".wav", "audio/x-wav": ".wav",
	"application/pdf": ".pdf", "text/plain": ".txt", "text/csv": ".csv", "application/zip": ".zip",
	"application/msword": ".doc", "application/vnd.ms-excel": ".xls", "application/vnd.ms-powerpoint": ".ppt",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   ".docx",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         ".xlsx",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": ".pptx",
	"application/vnd.oasis.opendocument.text":                                   ".odt",
	"application/vnd.oasis.opendocument.spreadsheet":                            ".ods",
}

// extension returns the cache extension of a declared MIME type.
func extension(declared string) string {
	t, _, err := mime.ParseMediaType(declared)
	if ext, ok := extensions[t]; err == nil && ok {
		return ext
	}
	return ".bin"
}

// mediaHandler downloads, caches, opens and saves media.
type mediaHandler struct {
	p    *pipeline
	o    mediaOptions
	pool *jobs.Pool

	evictMu sync.Mutex // one eviction at a time
}

func newMediaHandler(p *pipeline, o mediaOptions) *mediaHandler {
	if o.start == nil {
		o.start = startDetached
	}
	return &mediaHandler{p: p, o: o, pool: jobs.New(jobs.Options{Name: "media", Workers: 2, Timeout: 10 * time.Minute, Retries: -1, Log: p.log})}
}

func (h *mediaHandler) close() { h.pool.Close() }

// The temporary files in the media cache: downloads, encrypted uploads
// and attachments being copied in. A crash leaves them behind for the
// next start to remove (removeLeftovers).
const (
	downloadTemp = ".download-"
	uploadTemp   = ".upload-"
	adoptTemp    = ".adopt-"
)

// cachePath returns the path of a cache file from its stored name, which
// tarcin chose (sha256.ext). Any other name, such as one a damaged
// database holds, gives no path, so that no path leaves the cache.
func (o *mediaOptions) cachePath(name string) (string, bool) {
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		return "", false
	}
	return filepath.Join(o.Dir, name), true
}

// isMediaTemp reports whether a cache file name is a temporary file's.
func isMediaTemp(name string) bool {
	return strings.HasPrefix(name, downloadTemp) || strings.HasPrefix(name, uploadTemp) || strings.HasPrefix(name, adoptTemp)
}

// errNoMedia is returned for a message whose media is not stored.
var errNoMedia = errors.New("this message has no media to download")

// local returns the cache file of a message's media, downloading it if
// needed. Requests for one message share a download.
func (h *mediaHandler) local(ctx context.Context, msg *domain.Message) (string, domain.Media, error) {
	var path string
	var m domain.Media
	err := h.pool.Do(ctx, fmt.Sprintf("media %d %s", msg.ChatID, msg.WAID), func(ctx context.Context) error {
		var err error
		path, m, err = h.download(ctx, msg)
		return err
	})
	if path == "" && err == nil {
		// Another caller's download: it is in the store now.
		path, m, err = h.download(ctx, msg)
	}
	return path, m, err
}

// download makes sure the media is in the cache and returns its path.
func (h *mediaHandler) download(ctx context.Context, msg *domain.Message) (string, domain.Media, error) {
	src, ok, err := h.p.reader.MediaSource(ctx, msg.ChatID, msg.WAID)
	switch {
	case err != nil:
		return "", domain.Media{}, err
	case !ok:
		return "", domain.Media{}, errNoMedia
	case src.Media.Size > h.o.Limit:
		return "", src.Media, backend.ErrMediaTooLarge
	}
	if path, ok := h.o.cachePath(src.LocalName); ok {
		if _, err := os.Stat(path); err == nil {
			now := time.Now()
			_ = os.Chtimes(path, now, now) // recently used: evicted last
			return path, src.Media, nil
		}
	}
	dl, ok := h.p.opts.Backend.(backend.MediaDownloader)
	if !ok {
		return "", src.Media, errors.New("media cannot be downloaded here")
	}
	chat, err := h.p.chatJID(ctx, msg.ChatID)
	if err != nil {
		return "", src.Media, err
	}
	state := func(s domain.MediaState, sum []byte, name string) {
		ev := domain.MediaChanged{ChatJID: chat, TargetWAID: msg.WAID, State: s, SHA256: sum, LocalName: name}
		_ = h.p.writer.SubmitLive(context.WithoutCancel(ctx), []domain.Event{ev})
	}
	state(domain.MediaDownloading, nil, "")
	name, sum, err := h.fetch(ctx, dl, src)
	if err != nil {
		switch {
		case errors.Is(err, backend.ErrMediaGone):
			state(domain.MediaExpired, nil, "")
		default:
			state(domain.MediaFailed, nil, "")
		}
		return "", src.Media, err
	}
	state(domain.MediaLocal, sum, name)
	h.evict(ctx, name)
	return filepath.Join(h.o.Dir, name), src.Media, nil
}

// fetch downloads into a temporary file in the cache, syncs it and
// renames it to its content-addressed name.
func (h *mediaHandler) fetch(ctx context.Context, dl backend.MediaDownloader, src store.MediaSource) (string, []byte, error) {
	if err := os.MkdirAll(h.o.Dir, 0o700); err != nil {
		return "", nil, err
	}
	f, err := os.CreateTemp(h.o.Dir, downloadTemp+"*")
	if err != nil {
		return "", nil, err
	}
	tmp := f.Name()
	name, sum, fillErr := h.fill(ctx, dl, src, f)
	err = errors.Join(fillErr, f.Close())
	if err == nil {
		err = os.Rename(tmp, filepath.Join(h.o.Dir, name))
	}
	if err != nil {
		_ = os.Remove(tmp)
		return "", nil, err
	}
	return name, sum, nil
}

// fill downloads into f, syncs it, and names it by its content's hash.
func (h *mediaHandler) fill(ctx context.Context, dl backend.MediaDownloader, src store.MediaSource, f *os.File) (string, []byte, error) {
	if err := dl.DownloadMedia(ctx, src.Raw, f, h.o.Limit); err != nil {
		return "", nil, err
	}
	if err := f.Sync(); err != nil {
		return "", nil, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", nil, err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", nil, err
	}
	sum := hash.Sum(nil)
	return hex.EncodeToString(sum) + extension(src.Media.MIME), sum, nil
}

// evict deletes the least recently used cache files until the cache fits
// its cap, keeping the one just downloaded, and marks their media remote.
func (h *mediaHandler) evict(ctx context.Context, keep string) {
	h.evictMu.Lock()
	defer h.evictMu.Unlock()
	entries, err := os.ReadDir(h.o.Dir)
	if err != nil {
		return
	}
	type file struct {
		name string
		size int64
		used time.Time
	}
	var files []file
	var total int64
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		files = append(files, file{e.Name(), info.Size(), info.ModTime()})
		total += info.Size()
	}
	slices.SortFunc(files, func(a, b file) int { return a.used.Compare(b.used) })
	for _, f := range files {
		if total <= h.o.Cap {
			return
		}
		if f.name == keep {
			continue
		}
		if err := os.Remove(filepath.Join(h.o.Dir, f.name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			continue
		}
		total -= f.size
		_ = h.p.writer.SubmitLive(ctx, []domain.Event{domain.MediaEvicted{LocalName: f.name}})
	}
}

// opener returns the command that opens a MIME type: the opener for the
// exact type, else for "type/*", else the default.
func (h *mediaHandler) opener(declared string) []string {
	t, _, err := mime.ParseMediaType(declared)
	if err == nil {
		if argv, ok := h.o.Openers[t]; ok {
			return argv
		}
		if major, _, ok := strings.Cut(t, "/"); ok {
			if argv, ok := h.o.Openers[major+"/*"]; ok {
				return argv
			}
		}
	}
	return h.o.Open
}

// startDetached runs a program without a shell and without the terminal,
// and reaps it when it exits. It outlives tarcin's contexts: the user
// closes it.
func startDetached(argv []string) error {
	cmd := exec.CommandContext(context.Background(), argv[0], argv[1:]...) //nolint:gosec // the user's configured opener, and a cache path
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// openMedia returns the ui.Options.OpenMedia function: it downloads the
// media if needed and hands it to the opener.
func (h *mediaHandler) openMedia(msg domain.Message) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		path, m, err := h.local(ctx, &msg)
		if err != nil {
			return ui.BatchMsg{Notice: mediaProblem(err, h.o.Limit)}
		}
		argv := append(slices.Clone(h.opener(m.MIME)), path)
		if err := h.o.start(argv); err != nil {
			return ui.BatchMsg{Notice: "Could not open it with " + argv[0] + ": " + err.Error()}
		}
		return ui.BatchMsg{Notice: "Opened with " + filepath.Base(argv[0])}
	}
}

// saveMedia returns the ui.Options.SaveMedia function: it downloads the
// media if needed and copies it to the save directory under a safe name,
// never over an existing file.
func (h *mediaHandler) saveMedia(msg domain.Message) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		path, m, err := h.local(ctx, &msg)
		if err != nil {
			return ui.BatchMsg{Notice: mediaProblem(err, h.o.Limit)}
		}
		dst, err := saveCopy(path, h.o.SaveDir, saveName(&msg, &m))
		if err != nil {
			return ui.BatchMsg{Notice: "Not saved: " + err.Error()}
		}
		return ui.BatchMsg{Notice: "Saved to " + homeRelative(dst)}
	}
}

func mediaProblem(err error, limit int64) string {
	switch {
	case errors.Is(err, backend.ErrMediaTooLarge):
		return fmt.Sprintf("Not downloaded: larger than the %d MiB limit (media.max_download_mib)", limit>>20)
	case errors.Is(err, backend.ErrMediaGone):
		return "This media is no longer on WhatsApp's servers"
	case errors.Is(err, backend.ErrNotConnected):
		return "Not downloaded: offline"
	case errors.Is(err, errNoMedia):
		return "This message has no media"
	}
	return "Not downloaded: " + err.Error()
}

// saveName is the file name a saved attachment gets: the document's own
// name, or the kind and time; made safe as a single file name, with the
// cache's extension if it has none.
func saveName(msg *domain.Message, m *domain.Media) string {
	name := m.FileName
	if name == "" {
		name = "tarcin " + msg.Kind.String() + " " + msg.Timestamp.Local().Format("2006-01-02 15-04-05")
	}
	// The last path element only, of either kind of separator; then no
	// control, bidi or zero-width character, and no leading dot.
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.ReplaceAll(sanitize.Line(name), "\x00", "")
	name = strings.TrimLeft(strings.TrimSpace(name), ". ")
	if name == "" || name == "." {
		name = "media"
	}
	if filepath.Ext(name) == "" {
		name += extension(m.MIME)
	}
	if len(name) > 200 { // most file systems allow 255 bytes
		ext := filepath.Ext(name)
		name = strings.ToValidUTF8(name[:200-len(ext)], "") + ext
	}
	return name
}

// saveCopy copies src into dir as name, or "name (1).ext" and so on if
// that exists: O_EXCL never replaces a file. It returns the path.
func saveCopy(src, dir, name string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	in, err := os.Open(src) //nolint:gosec // a cache file
	if err != nil {
		return "", err
	}
	defer func() { _ = in.Close() }()
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 0; i < 1000; i++ {
		try := name
		if i > 0 {
			try = stem + " (" + strconv.Itoa(i) + ")" + ext
		}
		dst := filepath.Join(dir, try)
		out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // a sanitized name in the save directory
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, err = io.Copy(out, in)
		err = cmp.Or(err, out.Sync(), out.Close())
		if err != nil {
			_ = os.Remove(dst)
			return "", err
		}
		return dst, nil
	}
	return "", errors.New("too many files with that name")
}

// defaultSaveDir is $XDG_DOWNLOAD_DIR, or ~/Downloads.
func defaultSaveDir() string {
	if d := os.Getenv("XDG_DOWNLOAD_DIR"); filepath.IsAbs(d) {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return os.TempDir()
	}
	return filepath.Join(home, "Downloads")
}

// expandHome turns a leading "~/" into the home directory.
func expandHome(path string) string {
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, rest)
		}
	}
	return path
}

// attachment is a file the user sends, as examined before it is queued.
type attachment struct {
	path          string
	kind          domain.MessageKind
	mime          string
	name          string // the file's base name, for documents
	size          int64
	width, height int
}

// examine checks a file to send and decides how: an image, a video, an
// audio file or a document, by its content and its extension.
func (h *mediaHandler) examine(path string) (attachment, error) {
	path = expandHome(path)
	info, err := os.Stat(path)
	switch {
	case err != nil:
		return attachment{}, err
	case !info.Mode().IsRegular():
		return attachment{}, fmt.Errorf("%s is not a file", filepath.Base(path))
	case info.Size() == 0:
		return attachment{}, fmt.Errorf("%s is empty", filepath.Base(path))
	case info.Size() > h.o.UploadLimit:
		return attachment{}, fmt.Errorf("%s is larger than the %d MiB limit (media.max_upload_mib)", filepath.Base(path), h.o.UploadLimit>>20)
	}
	f, err := os.Open(path) //nolint:gosec // the file the user chose to send
	if err != nil {
		return attachment{}, err
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	a := attachment{path: path, size: info.Size(), name: filepath.Base(path)}
	a.mime = sniffType(path, head[:n])
	a.kind = sendKind(a.mime)
	if a.kind == domain.KindImage {
		if _, err := f.Seek(0, io.SeekStart); err == nil {
			if c, _, err := image.DecodeConfig(f); err == nil {
				a.width, a.height = c.Width, c.Height
			}
		}
	}
	return a, nil
}

// sniffType returns a file's MIME type: from its content for the media
// WhatsApp shows inline, else from its extension.
func sniffType(path string, head []byte) string {
	sniffed, _, _ := mime.ParseMediaType(http.DetectContentType(head))
	switch sniffed {
	case "image/jpeg", "image/png", "image/webp", "video/mp4", "audio/ogg", "audio/mpeg":
		return sniffed
	}
	if t := mime.TypeByExtension(strings.ToLower(filepath.Ext(path))); t != "" {
		if parsed, _, err := mime.ParseMediaType(t); err == nil {
			return parsed
		}
	}
	return "application/octet-stream"
}

// sendKind is how a MIME type is sent: inline media where WhatsApp plays
// it, a document otherwise.
func sendKind(mimeType string) domain.MessageKind {
	switch mimeType {
	case "image/jpeg", "image/png", "image/webp":
		return domain.KindImage
	case "video/mp4":
		return domain.KindVideo
	case "audio/ogg", "audio/mpeg", "audio/mp4", "audio/aac":
		return domain.KindAudio
	}
	return domain.KindDocument
}

// adopt copies a file this account sends into the cache under its
// content-addressed name, so that the sent message opens without a
// download.
func (h *mediaHandler) adopt(path, mimeType string) (string, []byte, error) {
	if err := os.MkdirAll(h.o.Dir, 0o700); err != nil {
		return "", nil, err
	}
	in, err := os.Open(path) //nolint:gosec // the file the user sends
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = in.Close() }()
	f, err := os.CreateTemp(h.o.Dir, adoptTemp+"*")
	if err != nil {
		return "", nil, err
	}
	tmp := f.Name()
	hash := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, hash), in)
	err = errors.Join(err, f.Sync(), f.Close())
	sum := hash.Sum(nil)
	name := hex.EncodeToString(sum) + extension(mimeType)
	if err == nil {
		err = os.Rename(tmp, filepath.Join(h.o.Dir, name))
	}
	if err != nil {
		_ = os.Remove(tmp)
		return "", nil, err
	}
	h.evict(context.Background(), name)
	return name, sum, nil
}

// attach is the ui.Options.Attach function: it examines a file and
// queues it to be sent to a chat, with the composer's text as caption.
func (p *pipeline) attach(s *sender, b backend.Backend) func(ui.Outgoing, string) tea.Cmd {
	if p.media == nil && p.opts.Media == nil {
		return nil
	}
	if _, ok := b.(backend.MediaSender); !ok {
		return nil
	}
	return func(out ui.Outgoing, path string) tea.Cmd {
		return func() tea.Msg {
			a, err := p.media.examine(path)
			if err != nil {
				return ui.AttachResultMsg{Chat: out.Chat, Err: err}
			}
			s.enqueue(sendJob{chat: out.Chat, waid: b.NewMessageID(), body: out.Text, quoted: out.Quoted, mentions: out.Mentions,
				at: p.clock.Now(), media: &a})
			return ui.AttachResultMsg{Chat: out.Chat}
		}
	}
}
