package wecom

import (
	"context"
	"encoding/base64"
	"errors"
	"log"
	"mime"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona-extensions/extensions/wecom/internal/wecom/api"
	"github.com/nwverse/persona/sdk/go/wire"
)

const (
	// imagesMost is how many pictures one message brings the hub, imageMost and fileMost how large each may be
	// (docs/extensions/channels.md "Protocol" in Persona).
	imagesMost  = 4
	imageMost   = 3 << 20
	fileMost    = 50 << 20
	maxSeen     = 300
	strangerGap = 10 * time.Minute
)

// sendRetryWait is the first wait before sending a piece again (then twice, three times as long); tests shorten it.
var sendRetryWait = time.Second

// handle takes one message to the robot. Groups are never handled; in a one-to-one chat the owner's message goes to
// the hub, a pairing code pairs, and anyone else hears whose robot it is.
func (b *Bot) handle(ctx context.Context, c *api.Client, m api.Message) {
	if m.ChatType != api.ChatSingle || m.From.UserID == "" {
		return
	}
	b.mu.Lock()
	if slices.Contains(b.st.Seen, m.ID) {
		b.mu.Unlock()
		return
	}
	b.st.Seen = append(b.st.Seen, m.ID)
	if len(b.st.Seen) > maxSeen {
		b.st.Seen = b.st.Seen[len(b.st.Seen)-maxSeen:]
	}
	o := b.st.Owner
	if o != nil && m.From.UserID == o.UserID {
		b.last = b.opt.Now()
		b.saveLocked(ctx)
		b.mu.Unlock()
		b.forward(ctx, c, m)
		return
	}
	reply, handled := b.tryPairLocked(ctx, m.From.UserID, m.PlainText())
	b.saveLocked(ctx)
	b.mu.Unlock()
	if handled {
		b.tell(ctx, m.From.UserID, reply)
		return
	}
	b.stranger(ctx, m.From.UserID, o == nil)
}

// forward hands the owner's message to the hub.
func (b *Bot) forward(ctx context.Context, c *api.Client, m api.Message) {
	msg := b.compose(ctx, c, m)
	if empty(msg) {
		return
	}
	if b.opt.Hub == nil {
		log.Printf("wecom: no hub to hand the message to")
		return
	}
	if err := b.opt.Hub.Receive(ctx, msg); err != nil {
		log.Printf("wecom: handing a message to the hub: %v", err)
		b.tell(ctx, m.From.UserID, t(b.lang(), "chat.hub_error"))
	}
}

// stranger: the robot only serves the person who paired it; anyone else hears so (or how to pair, before anyone
// has), at most once in ten minutes.
func (b *Bot) stranger(ctx context.Context, who string, unpaired bool) {
	b.mu.Lock()
	last, ok := b.replied[who]
	now := b.opt.Now()
	if ok && now.Sub(last) < strangerGap {
		b.mu.Unlock()
		return
	}
	b.replied[who] = now
	for w, at := range b.replied {
		if now.Sub(at) >= strangerGap {
			delete(b.replied, w)
		}
	}
	b.mu.Unlock()
	key := "chat.stranger"
	if unpaired {
		key = "chat.not_paired"
	}
	b.tell(ctx, who, t(b.lang(), key))
}

// tell sends a line to someone who just wrote to the robot. A failure is logged (never the text).
func (b *Bot) tell(ctx context.Context, userID, text string) {
	b.mu.Lock()
	s := b.session
	b.mu.Unlock()
	if s == nil {
		return
	}
	if err := s.SendMarkdown(ctx, userID, text); err != nil {
		log.Printf("wecom: answering in the chat: %v", err)
	}
}

// Send is the hub's channel.send: Markdown to the owner, in their chat with the robot. Before anyone paired, or
// while not connected, there is nowhere to send to: errCannotSend.
func (b *Bot) Send(ctx context.Context, p wire.ChannelSendParams) (wire.ChannelSendResult, error) {
	b.mu.Lock()
	s, o := b.session, b.st.Owner
	b.mu.Unlock()
	if s == nil || o == nil {
		return wire.ChannelSendResult{}, errCannotSend
	}
	for _, part := range Chunk(strings.TrimSpace(p.Text), MaxText) {
		var err error
		for attempt := range 3 {
			if err = s.SendMarkdown(ctx, o.UserID, part); err == nil || !retryable(err) {
				break
			}
			if !sleep(ctx, time.Duration(attempt+1)*sendRetryWait) {
				return wire.ChannelSendResult{}, ctx.Err()
			}
		}
		if err != nil {
			log.Printf("wecom: sending a message: %v", err)
			return wire.ChannelSendResult{}, sendFailed{b.lang()}
		}
	}
	return wire.ChannelSendResult{}, nil
}

// retryable: no answer in time (WeCom's refusals, with an errcode, are not tried again).
func retryable(err error) bool {
	var e *api.Error
	return !errors.As(err, &e) && errors.Is(err, context.DeadlineExceeded)
}

// sendFailed: WeCom did not take the message (logged by the hub, which sends it again with the same ref).
type sendFailed struct{ lang string }

func (e sendFailed) Error() string { return t(e.lang, "chat.send_failed") }

// empty says whether m has nothing to hand on.
func empty(m wire.ChannelMessage) bool {
	return strings.TrimSpace(m.Text) == "" && len(m.Images) == 0 && len(m.Files) == 0 && m.Quote == "" && m.Forward == "" &&
		len(m.Unsupported) == 0
}

// compose is the owner's message as the hub takes it: the text (a voice message's transcript), the quote, the
// pictures and the files; what cannot be brought is named for the assistant.
func (b *Bot) compose(ctx context.Context, c *api.Client, m api.Message) wire.ChannelMessage {
	out := wire.ChannelMessage{ID: m.ID, Text: m.PlainText(), Quote: m.QuoteText()}
	for _, p := range m.Parts() {
		switch p.Kind {
		case api.KindText:
		case api.KindVoice:
			if strings.TrimSpace(p.Text) == "" {
				out.Unsupported = append(out.Unsupported, "一段没有转写的语音") // i18n-ignore: what the assistant reads
			}
		case api.KindImage:
			if len(out.Images) >= imagesMost {
				out.Unsupported = append(out.Unsupported, "更多的图片") // i18n-ignore: what the assistant reads
				continue
			}
			data, _, err := c.Download(ctx, p.URL, p.AESKey, imageMost)
			switch {
			case errors.Is(err, api.ErrTooLarge):
				out.Unsupported = append(out.Unsupported, "一张太大的图片") // i18n-ignore: what the assistant reads
			case err != nil:
				log.Printf("wecom: downloading a picture: %v", err)
				out.Unsupported = append(out.Unsupported, "一张没下载下来的图片") // i18n-ignore: what the assistant reads
			default:
				out.Images = append(out.Images, wire.ChannelBlob{Mime: http.DetectContentType(data), Data: base64.StdEncoding.EncodeToString(data)})
			}
		case api.KindFile:
			data, given, err := c.Download(ctx, p.URL, p.AESKey, fileMost)
			name := p.FileName
			if name == "" {
				name = given
			}
			switch {
			case errors.Is(err, api.ErrTooLarge):
				out.Unsupported = append(out.Unsupported, fileLabel(name)+"（超过 50 MB）") // i18n-ignore: what the assistant reads
			case err != nil:
				log.Printf("wecom: downloading a file: %v", err)
				out.Unsupported = append(out.Unsupported, "没下载下来的"+fileLabel(name)) // i18n-ignore: what the assistant reads
			default:
				if name == "" {
					name = "file"
				}
				out.Files = append(out.Files, wire.ChannelBlob{Name: name, Mime: fileMime(name, data), Data: base64.StdEncoding.EncodeToString(data)})
			}
		case api.KindVideo:
			out.Unsupported = append(out.Unsupported, "一段视频") // i18n-ignore: what the assistant reads
		default:
			out.Unsupported = append(out.Unsupported, "一条暂不支持的企业微信消息") // i18n-ignore: what the assistant reads
		}
	}
	out.Unsupported = slices.Compact(out.Unsupported)
	return out
}

// fileMime is a file's type: from its bytes, else from its name's extension.
func fileMime(name string, data []byte) string {
	if m := http.DetectContentType(data); m != "application/octet-stream" {
		return m
	}
	if m := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); m != "" {
		return m
	}
	return "application/octet-stream"
}

func fileLabel(name string) string {
	if name == "" {
		return "一个文件" // i18n-ignore: what the assistant reads
	}
	return "文件「" + name + "」" // i18n-ignore: what the assistant reads
}

// Chunk cuts text into pieces of at most most characters, at a line break when there is one in the second half of
// a piece. The hub already cuts at MaxText; this only guards what it did not.
func Chunk(text string, most int) []string {
	if text == "" {
		return nil
	}
	var out []string
	for utf8.RuneCountInString(text) > most {
		r := []rune(text)
		cut := most
		for i := most - 1; i >= most/2; i-- {
			if r[i] == '\n' {
				cut = i + 1
				break
			}
		}
		out = append(out, strings.TrimRight(string(r[:cut]), "\n"))
		text = strings.TrimLeft(string(r[cut:]), "\n")
	}
	if text != "" {
		out = append(out, text)
	}
	return out
}
