// SPDX-License-Identifier: GPL-3.0-or-later

package wa

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"github.com/Yaman-yagiz/tarcin/internal/backend"
	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

var _ backend.MediaDownloader = (*Adapter)(nil)

// mediaOverhead is how much larger the encrypted file is than the
// content: CBC padding and the MAC.
const mediaOverhead = 64

// DownloadMedia implements backend.MediaDownloader. The size the message
// declares is checked first; the size written is checked as the download
// streams, so a lying sender cannot fill the disk.
func (a *Adapter) DownloadMedia(ctx context.Context, raw []byte, f *os.File, limit int64) error {
	var msg waE2E.Message
	if len(raw) == 0 || proto.Unmarshal(raw, &msg) != nil {
		return errors.New("the message's media details are not stored")
	}
	dl, size := downloadable(&msg)
	switch {
	case dl == nil:
		return whatsmeow.ErrNothingDownloadableFound
	case limit < 0 || size > uint64(limit):
		return backend.ErrMediaTooLarge
	case !a.cli.IsConnected():
		return backend.ErrNotConnected
	}
	err := a.cli.DownloadToFile(ctx, dl, &limitedFile{File: f, limit: limit + mediaOverhead})
	switch {
	case errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith404), errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith410):
		return fmt.Errorf("%w: %w", backend.ErrMediaGone, err)
	case errors.Is(err, errTooLarge):
		return backend.ErrMediaTooLarge
	case err != nil:
		return fmt.Errorf("downloading the media: %w", err)
	}
	return nil
}

// downloadable returns the part of a message that holds media, and the
// size it declares.
func downloadable(m *waE2E.Message) (whatsmeow.DownloadableMessage, uint64) {
	switch {
	case m.GetImageMessage() != nil:
		return m.GetImageMessage(), m.GetImageMessage().GetFileLength()
	case m.GetVideoMessage() != nil:
		return m.GetVideoMessage(), m.GetVideoMessage().GetFileLength()
	case m.GetPtvMessage() != nil:
		return m.GetPtvMessage(), m.GetPtvMessage().GetFileLength()
	case m.GetAudioMessage() != nil:
		return m.GetAudioMessage(), m.GetAudioMessage().GetFileLength()
	case m.GetDocumentMessage() != nil:
		return m.GetDocumentMessage(), m.GetDocumentMessage().GetFileLength()
	case m.GetStickerMessage() != nil:
		return m.GetStickerMessage(), m.GetStickerMessage().GetFileLength()
	}
	return nil, 0
}

var errTooLarge = errors.New("the media grew past the limit")

// limitedFile is a file that refuses to grow past limit bytes, through
// any of the ways whatsmeow writes, seeks or truncates it.
type limitedFile struct {
	*os.File
	limit int64
	pos   int64
}

func (l *limitedFile) Write(p []byte) (int, error) {
	if l.pos+int64(len(p)) > l.limit {
		return 0, errTooLarge
	}
	n, err := l.File.Write(p)
	l.pos += int64(n)
	return n, err
}

// WriteString and ReadFrom go through Write: the ones *os.File has would
// write past the limit, and io.Copy prefers ReadFrom.
func (l *limitedFile) WriteString(s string) (int, error) {
	return l.Write([]byte(s)) //nolint:gocritic // going through Write is the point
}

func (l *limitedFile) ReadFrom(r io.Reader) (int64, error) {
	return io.Copy(struct{ io.Writer }{l}, r) // hides ReadFrom from io.Copy
}

func (l *limitedFile) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 || off+int64(len(p)) > l.limit {
		return 0, errTooLarge
	}
	return l.File.WriteAt(p, off)
}

func (l *limitedFile) Seek(offset int64, whence int) (int64, error) {
	pos, err := l.File.Seek(offset, whence)
	if err == nil {
		l.pos = pos
	}
	return pos, err
}

func (l *limitedFile) Truncate(size int64) error {
	if size > l.limit {
		return errTooLarge
	}
	return l.File.Truncate(size)
}

var (
	_ io.WriterAt     = (*limitedFile)(nil)
	_ io.ReaderFrom   = (*limitedFile)(nil)
	_ io.StringWriter = (*limitedFile)(nil)
)

var _ backend.MediaSender = (*Adapter)(nil)

// SendMedia implements backend.MediaSender: UploadReader streams the file
// through req.Temp, so it is never held in memory.
func (a *Adapter) SendMedia(ctx context.Context, req backend.MediaRequest) (backend.SendResult, []byte, error) {
	to, err := toJID(req.ChatJID)
	if err != nil {
		return backend.SendResult{}, nil, err
	}
	if !a.cli.IsConnected() {
		return backend.SendResult{}, nil, backend.ErrNotConnected
	}
	kind := map[domain.MessageKind]whatsmeow.MediaType{
		domain.KindImage: whatsmeow.MediaImage, domain.KindVideo: whatsmeow.MediaVideo,
		domain.KindAudio: whatsmeow.MediaAudio, domain.KindDocument: whatsmeow.MediaDocument,
	}
	mt, ok := kind[req.Kind]
	if !ok {
		return backend.SendResult{}, nil, fmt.Errorf("%s cannot be sent as media", req.Kind)
	}
	up, err := a.cli.UploadReader(ctx, req.File, req.Temp, mt)
	if err != nil {
		return backend.SendResult{}, nil, fmt.Errorf("uploading: %w", err)
	}
	msg := mediaMessage(&req, &up)
	if req.Expiration > 0 {
		setExpiration(msg, req.Expiration)
	}
	resp, err := a.cli.SendMessage(ctx, to, msg, whatsmeow.SendRequestExtra{ID: types.MessageID(req.WAID)})
	if err != nil {
		return backend.SendResult{}, nil, fmt.Errorf("sending: %w", err)
	}
	raw, err := proto.Marshal(msg)
	if err != nil {
		return backend.SendResult{Timestamp: resp.Timestamp}, nil, nil
	}
	return backend.SendResult{Timestamp: resp.Timestamp}, raw, nil
}

// setExpiration gives a media message the chat's disappearing timer.
func setExpiration(m *waE2E.Message, d time.Duration) {
	ctx := &waE2E.ContextInfo{Expiration: expiration(d)}
	switch {
	case m.ImageMessage != nil:
		m.ImageMessage.ContextInfo = ctx
	case m.VideoMessage != nil:
		m.VideoMessage.ContextInfo = ctx
	case m.AudioMessage != nil:
		m.AudioMessage.ContextInfo = ctx
	case m.DocumentMessage != nil:
		m.DocumentMessage.ContextInfo = ctx
	}
}

// mediaMessage builds the protocol message of an uploaded attachment.
func mediaMessage(req *backend.MediaRequest, up *whatsmeow.UploadResponse) *waE2E.Message {
	caption := proto.String(req.Caption)
	if req.Caption == "" {
		caption = nil
	}
	dim := func(n int) *uint32 {
		if n <= 0 || n > math.MaxInt32 {
			return nil
		}
		return proto.Uint32(uint32(n))
	}
	secs := proto.Uint32(uint32(min(max(req.Duration.Seconds(), 0), math.MaxInt32)))
	switch req.Kind {
	case domain.KindImage:
		return &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: caption, Mimetype: proto.String(req.MIME),
			URL: &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256,
			FileSHA256: up.FileSHA256, FileLength: &up.FileLength, Width: dim(req.Width), Height: dim(req.Height)}}
	case domain.KindVideo:
		return &waE2E.Message{VideoMessage: &waE2E.VideoMessage{Caption: caption, Mimetype: proto.String(req.MIME),
			URL: &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256,
			FileSHA256: up.FileSHA256, FileLength: &up.FileLength, Width: dim(req.Width), Height: dim(req.Height), Seconds: secs}}
	case domain.KindAudio:
		return &waE2E.Message{AudioMessage: &waE2E.AudioMessage{Mimetype: proto.String(req.MIME),
			URL: &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256,
			FileSHA256: up.FileSHA256, FileLength: &up.FileLength, Seconds: secs}}
	}
	name := proto.String(req.FileName)
	return &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{Caption: caption, Mimetype: proto.String(req.MIME),
		FileName: name, Title: name, URL: &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey,
		FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: &up.FileLength}}
}
