package wechat

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"log"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona-extensions/extensions/wechat/internal/wechat/ilink"
	"github.com/nwverse/persona/sdk/go/wire"
)

const (
	// imagesMost is how many pictures one message brings the hub (docs/extensions/channels.md "Protocol").
	imagesMost = 4
	// messageMediaMost is the hub's raw image + file budget for one message, before Base64 reaches the process RPC.
	messageMediaMost = ilink.MaxFile
	// typingEvery is how often "typing…" is shown again while the hub keeps asking for it.
	typingEvery = 5 * time.Second
	maxSeen     = 200
	strangerGap = 10 * time.Minute
	// typingFor: "typing…" is let go when the hub has not asked for it again in this long (it asks every 5 s while a
	// turn goes on): a hub that went away does not leave it on.
	typingFor = 30 * time.Second
)

// sendRetryWait is the first wait before sending a piece again (then twice, three times as long); tests shorten it.
var sendRetryWait = time.Second

// handle takes one message the bot got: someone else's gets one line saying whose bot it is; the owner's goes to the
// hub, which knows what it is (a command, an answer to what was asked, a message for the conversation).
func (b *Bot) handle(ctx context.Context, c *ilink.Client, creds ilink.Credentials, m ilink.Message) {
	owner := m.From == creds.UserID
	b.mu.Lock()
	if slices.Contains(b.st.Seen, m.ID) {
		b.mu.Unlock()
		return
	}
	b.st.Seen = append(b.st.Seen, m.ID)
	if len(b.st.Seen) > maxSeen {
		b.st.Seen = b.st.Seen[len(b.st.Seen)-maxSeen:]
	}
	b.status.LastEvent = b.opt.Now()
	if owner {
		b.st.Context = m.ContextToken
	}
	b.saveLocked(ctx)
	b.mu.Unlock()
	if !owner {
		b.stranger(ctx, c, m)
		return
	}
	msg := b.compose(ctx, c, m)
	if empty(msg) {
		return
	}
	if b.opt.Hub == nil {
		log.Printf("wechat: no hub to hand the message to")
		return
	}
	if err := b.opt.Hub.Receive(ctx, msg); err != nil {
		log.Printf("wechat: handing a message to the hub: %v", err)
		b.send(ctx, c, m.From, m.ContextToken, m.ID, "", t(b.lang(), "chat.hub_error"))
	}
}

// stranger: the bot only serves the WeChat that bound it; anyone else hears so, at most once in ten minutes.
func (b *Bot) stranger(ctx context.Context, c *ilink.Client, m ilink.Message) {
	b.mu.Lock()
	last, ok := b.replied[m.From]
	now := b.opt.Now()
	if ok && now.Sub(last) < strangerGap {
		b.mu.Unlock()
		return
	}
	b.replied[m.From] = now
	for who, at := range b.replied {
		if now.Sub(at) >= strangerGap {
			delete(b.replied, who)
		}
	}
	b.mu.Unlock()
	b.send(ctx, c, m.From, m.ContextToken, m.ID, "", t(b.lang(), "chat.stranger"))
}

// send sends text to peer as the bot: WeChat's subset of Markdown, in pieces it takes. Every piece's final result is
// logged with its inbound message, ref and duration (never the text). ref, when given, is the hub's id for the message:
// each piece's client_id comes from it, so a retry (this loop's or the hub's) is kept once by iLink; without one, each
// piece gets a random id.
func (b *Bot) send(ctx context.Context, c *ilink.Client, peer, contextToken, replyTo, ref, text string) bool {
	if c == nil || contextToken == "" {
		log.Printf("wechat: nothing to send with (no client or context token)")
		return false
	}
	parts := ilink.Chunk(ilink.FilterMarkdown(strings.TrimSpace(text)), 0)
	for i, part := range parts {
		started := time.Now()
		var err error
		id := clientID(ref, i) // the same for every attempt: a retry after a lost answer must not post it again
		for attempt := range 3 {
			if err = c.SendText(ctx, peer, contextToken, id, part); err == nil || !ilink.Retryable(err) {
				break
			}
			if !sleep(ctx, time.Duration(attempt+1)*sendRetryWait) {
				err = ctx.Err()
				break
			}
		}
		took := time.Since(started)
		if err != nil {
			log.Printf("wechat: send reply_to=%q ref=%q part=%d/%d result=failure duration=%s error_type=%s error=%v",
				replyTo, ref, i+1, len(parts), took, ilink.ErrorType(err), err)
			return false
		}
		log.Printf("wechat: send reply_to=%q ref=%q part=%d/%d result=success duration=%s",
			replyTo, ref, i+1, len(parts), took)
	}
	return true
}

// Send is the hub's channel.send: text to the owner, in the chat of their latest message. Before the owner has
// written to the bot there is no context token, and nothing can be sent: errCannotSend.
func (b *Bot) Send(ctx context.Context, p wire.ChannelSendParams) (wire.ChannelSendResult, error) {
	b.mu.Lock()
	c, creds, contextToken := b.client, b.st.Creds, b.st.Context
	b.mu.Unlock()
	if creds == nil || c == nil || contextToken == "" {
		return wire.ChannelSendResult{}, errCannotSend
	}
	if !b.send(ctx, c, creds.UserID, contextToken, p.ReplyTo, p.Ref, p.Text) {
		return wire.ChannelSendResult{}, sendFailed{b.lang()}
	}
	return wire.ChannelSendResult{}, nil
}

// sendFailed: WeChat did not take the message (logged by the hub, which sends it again with the same ref).
type sendFailed struct{ lang string }

func (e sendFailed) Error() string { return t(e.lang, "chat.send_failed") }

// empty says whether m has nothing to hand on.
func empty(m wire.ChannelMessage) bool {
	return strings.TrimSpace(m.Text) == "" && len(m.Images) == 0 && len(m.Files) == 0 && m.Quote == "" && m.Forward == "" &&
		len(m.Unsupported) == 0
}

// clientID is piece i's iLink client_id: derived from the hub's ref when there is one, random otherwise.
func clientID(ref string, i int) string {
	if ref == "" {
		var r [12]byte
		_, _ = rand.Read(r[:])
		return "persona-" + hex.EncodeToString(r[:])
	}
	sum := sha256.Sum256([]byte(ref + "#" + strconv.Itoa(i)))
	return "persona-" + hex.EncodeToString(sum[:12])
}

// compose is the owner's message as the hub takes it: the text (a voice message's transcript), the quote, the
// pictures, and downloaded documents and videos as files.
func (b *Bot) compose(ctx context.Context, c *ilink.Client, m ilink.Message) wire.ChannelMessage {
	out := wire.ChannelMessage{ID: m.ID, Text: strings.TrimSpace(m.Text), Quote: m.Quote}
	total := 0
	for _, md := range m.Media {
		switch md.Kind {
		case ilink.MediaImage:
			if len(out.Images) >= imagesMost {
				out.Unsupported = append(out.Unsupported, "更多的图片") // i18n-ignore: what the assistant reads
				continue
			}
			data, err := c.Download(ctx, md)
			if err != nil {
				log.Printf("wechat: downloading a picture: %v", err)
				out.Unsupported = append(out.Unsupported, "一张没下载下来的图片") // i18n-ignore: what the assistant reads
				continue
			}
			if len(data) > messageMediaMost-total {
				out.Unsupported = append(out.Unsupported, "一张太大的图片") // i18n-ignore: what the assistant reads
				continue
			}
			out.Images = append(out.Images, wire.ChannelBlob{Mime: http.DetectContentType(data), Data: base64.StdEncoding.EncodeToString(data)})
			total += len(data)
		case ilink.MediaVoice:
			if out.Text == "" {
				out.Unsupported = append(out.Unsupported, "一段没有转写的语音") // i18n-ignore: what the assistant reads
			}
		case ilink.MediaFile, ilink.MediaVideo:
			name, unsupported := md.FileName, "一段视频" // i18n-ignore: what the assistant reads
			if md.Kind == ilink.MediaVideo {
				name = "video.mp4"
			} else if name == "" {
				name, unsupported = "file", "一个文件" // i18n-ignore: what the assistant reads
			} else {
				unsupported = "文件「" + name + "」" // i18n-ignore: what the assistant reads
			}
			if md.Size > int64(messageMediaMost-total) {
				out.Unsupported = append(out.Unsupported, unsupported)
				continue
			}
			data, err := c.Download(ctx, md)
			if err != nil {
				log.Printf("wechat: downloading a file: %v", err)
				out.Unsupported = append(out.Unsupported, unsupported)
				continue
			}
			if len(data) > messageMediaMost-total {
				out.Unsupported = append(out.Unsupported, unsupported)
				continue
			}
			out.Files = append(out.Files, wire.ChannelBlob{Name: name, Mime: fileMIME(md, data), Data: base64.StdEncoding.EncodeToString(data)})
			total += len(data)
		}
	}
	out.Unsupported = slices.Compact(out.Unsupported)
	return out
}

// fileMIME keeps the filename's document type; unknown extensions use the decrypted bytes. iLink videos are MP4.
func fileMIME(md ilink.Media, data []byte) string {
	if md.Kind == ilink.MediaVideo {
		return "video/mp4"
	}
	switch strings.ToLower(filepath.Ext(md.FileName)) {
	case ".pdf":
		return "application/pdf"
	case ".doc":
		return "application/msword"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	default:
		return http.DetectContentType(data)
	}
}

// typing is the owner's chat showing that the bot is writing.
type typing struct {
	ticket string
	at     time.Time // when the hub last asked for it
	shown  time.Time // when WeChat was last told
}

// Typing is the hub's channel.typing: on while a turn goes on (asked again every few seconds), off once answered.
func (b *Bot) Typing(ctx context.Context, on bool) error {
	b.mu.Lock()
	c, creds, contextToken, t := b.client, b.st.Creds, b.st.Context, b.typing
	now := b.opt.Now()
	if !on {
		b.typing = nil
	}
	b.mu.Unlock()
	if c == nil || creds == nil || contextToken == "" {
		return nil // presence is best effort
	}
	switch {
	case !on && t != nil:
		return c.SetTyping(ctx, creds.UserID, t.ticket, false)
	case !on:
		return nil
	case t != nil && now.Sub(t.at) < typingFor:
		b.mu.Lock()
		t.at = now
		b.mu.Unlock()
		if now.Sub(t.shown) < typingEvery {
			return nil
		}
		b.mu.Lock()
		t.shown = now
		b.mu.Unlock()
		return c.SetTyping(ctx, creds.UserID, t.ticket, true) // WeChat clears it on its own
	}
	ticket, err := c.TypingTicket(ctx, creds.UserID, contextToken)
	if err != nil {
		return nil
	}
	if err := c.SetTyping(ctx, creds.UserID, ticket, true); err != nil {
		return nil
	}
	b.mu.Lock()
	b.typing = &typing{ticket: ticket, at: now, shown: now}
	b.mu.Unlock()
	return nil
}
