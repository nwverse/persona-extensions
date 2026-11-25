package wechat

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
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
	msg := b.compose(ctx, c, creds.UserID, m)
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
	return b.sendParts(ctx, c, peer, contextToken, replyTo, ref, splitText(text), 0, nil)
}

// splitText is the pieces of text that WeChat takes, one message each.
func splitText(text string) []string {
	return ilink.Chunk(ilink.FilterMarkdown(strings.TrimSpace(text)), 0)
}

// sendParts sends parts[from:] one by one; parts before from were sent already. sent is told how many pieces are
// confirmed after each one that is (nil: nobody is told).
func (b *Bot) sendParts(ctx context.Context, c *ilink.Client, peer, contextToken, replyTo, ref string, parts []string, from int, sent func(done int)) bool {
	for i := from; i < len(parts); i++ {
		part := parts[i]
		started := time.Now()
		var err error
		var serverID string
		id := clientID(ref, i) // the same for every attempt: a retry after a lost answer must not post it again
		for attempt := range 3 {
			if serverID, err = c.SendTextWithID(ctx, peer, contextToken, id, part); err == nil || !ilink.Retryable(err) {
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
		b.rememberOutbound(peer, serverID, part)
		if sent != nil {
			sent(i + 1)
		}
	}
	return true
}

func (b *Bot) rememberOutbound(peer, serverID, text string) {
	if b.quotes == nil || serverID == "" {
		return
	}
	b.mu.Lock()
	owner := b.st.Creds != nil && b.st.Creds.UserID == peer
	b.mu.Unlock()
	if owner {
		b.quotes.put(quoteCacheID(peer, serverID), text, nil)
	}
}

// Send is the hub's channel.send: text to the owner, in the chat of their latest message, then its pictures. Before
// the owner has written to the bot there is no context token, and nothing can be sent: errCannotSend.
//
// The hub sends the same ref again when a send fails, so what WeChat confirmed is remembered by ref (sendProgress)
// and the next send of the ref goes on from the piece or picture that failed, not from the first piece.
func (b *Bot) Send(ctx context.Context, p wire.ChannelSendParams) (wire.ChannelSendResult, error) {
	b.mu.Lock()
	c, creds, contextToken := b.client, b.st.Creds, b.st.Context
	b.mu.Unlock()
	if creds == nil || c == nil || contextToken == "" {
		return wire.ChannelSendResult{}, errCannotSend
	}
	parts := splitText(p.Text)
	textDone, imagesDone := b.resume(p.Ref, len(parts), len(p.Images))
	if textDone > 0 || imagesDone > 0 {
		log.Printf("wechat: send reply_to=%q ref=%q resume text=%d/%d image=%d/%d",
			p.ReplyTo, p.Ref, textDone, len(parts), imagesDone, len(p.Images))
	}
	if !b.sendParts(ctx, c, creds.UserID, contextToken, p.ReplyTo, p.Ref, parts, textDone, func(done int) {
		b.noteSent(p.Ref, len(parts), len(p.Images), done, imagesDone)
	}) {
		return wire.ChannelSendResult{}, sendFailed{b.lang()}
	}
	for i := imagesDone; i < len(p.Images); i++ {
		if !b.sendImage(ctx, c, creds.UserID, contextToken, p.ReplyTo, p.Ref, clientID(p.Ref+"#image", i), i, len(p.Images), p.Images[i]) {
			return wire.ChannelSendResult{}, sendFailed{b.lang()}
		}
		b.noteSent(p.Ref, len(parts), len(p.Images), len(parts), i+1)
	}
	b.forget(p.Ref)
	return wire.ChannelSendResult{}, nil
}

// progressMost is how many refs' progress the bot keeps; past it the oldest is dropped (its send then starts over).
const progressMost = 64

// sendProgress is how far one ref's channel.send got: how many text pieces and pictures WeChat confirmed (a picture
// skipped as unsendable counts), and how many of each the send has, to tell that the hub sent something else under
// the same ref.
type sendProgress struct {
	parts, images         int
	partsDone, imagesDone int
}

// resume is how many text pieces and pictures of ref's send are confirmed already; none when ref is empty, nothing
// was kept, or what was kept was for another number of pieces or pictures (that is dropped).
func (b *Bot) resume(ref string, parts, images int) (partsDone, imagesDone int) {
	if ref == "" {
		return 0, 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	pr, ok := b.progress[ref]
	if !ok {
		return 0, 0
	}
	if pr.parts != parts || pr.images != images {
		b.forgetLocked(ref)
		return 0, 0
	}
	return pr.partsDone, pr.imagesDone
}

// noteSent records that partsDone text pieces and imagesDone pictures of ref's send are confirmed.
func (b *Bot) noteSent(ref string, parts, images, partsDone, imagesDone int) {
	if ref == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.progress == nil {
		b.progress = map[string]*sendProgress{}
	}
	pr, ok := b.progress[ref]
	if !ok {
		pr = &sendProgress{}
		b.progress[ref] = pr
		b.progressOrder = append(b.progressOrder, ref)
		for len(b.progressOrder) > progressMost {
			delete(b.progress, b.progressOrder[0])
			b.progressOrder = b.progressOrder[1:]
		}
	}
	*pr = sendProgress{parts: parts, images: images, partsDone: partsDone, imagesDone: imagesDone}
}

// forget drops ref's progress: its send is done (sentRefs remembers that from here on).
func (b *Bot) forget(ref string) {
	if ref == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.forgetLocked(ref)
}

func (b *Bot) forgetLocked(ref string) {
	delete(b.progress, ref)
	b.progressOrder = slices.DeleteFunc(b.progressOrder, func(r string) bool { return r == ref })
}

// sendImage uploads one of the hub's pictures (the i-th of n) to WeChat's CDN and sends it, trying a failure that may
// pass again twice more, after sendRetryWait and then twice that (UploadImage itself tries once). id is the same on
// every attempt and every send of the same ref. A picture that isn't one (not base64, empty, too large) is logged and
// skipped, not tried again. Its final result is logged like a text piece's, with image=i/n in place of part=i/n (never
// the picture, its upload URL or keys).
func (b *Bot) sendImage(ctx context.Context, c *ilink.Client, peer, contextToken, replyTo, ref, id string, i, n int, img wire.ChannelBlob) bool {
	started := time.Now()
	head := fmt.Sprintf("wechat: send reply_to=%q ref=%q image=%d/%d", replyTo, ref, i+1, n)
	data, err := base64.StdEncoding.DecodeString(img.Data)
	if err != nil || len(data) == 0 || len(data) > ilink.MaxMedia {
		reason := "not_base64"
		switch {
		case err == nil && len(data) == 0:
			reason = "empty"
		case err == nil:
			reason = "too_large"
		}
		log.Printf("%s result=skipped reason=%s duration=%s", head, reason, time.Since(started))
		return true
	}
	for attempt := range 3 {
		var up ilink.Uploaded
		if up, err = c.UploadImage(ctx, peer, data); err == nil {
			err = c.SendImage(ctx, peer, contextToken, id, up)
		}
		if err == nil || !ilink.Retryable(err) {
			break
		}
		if !sleep(ctx, time.Duration(attempt+1)*sendRetryWait) {
			err = ctx.Err()
			break
		}
	}
	took := time.Since(started)
	if err != nil {
		log.Printf("%s result=failure duration=%s error_type=%s error=%v", head, took, ilink.ErrorType(err), err)
		return false
	}
	log.Printf("%s result=success duration=%s", head, took)
	return true
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

// compose is the owner's message as the hub takes it: the text (including a voice message's transcript when WeChat
// provides one), the quote, the pictures, and downloaded documents, videos and untranscribed voice audio as files.
func (b *Bot) compose(ctx context.Context, c *ilink.Client, account string, m ilink.Message) wire.ChannelMessage {
	out := wire.ChannelMessage{ID: m.ID, Text: strings.TrimSpace(m.Text), Quote: m.Quote}
	if b.quotes != nil {
		media := make([]quoteMedia, 0, len(m.Media))
		for _, md := range m.Media {
			media = append(media, quoteMedia{Kind: string(md.Kind), Name: md.FileName, Size: md.Size})
		}
		b.quotes.put(quoteCacheID(account, m.SvrID), out.Text, media)
		restored := b.quotes.lookup(quoteCacheID(account, m.QuoteID))
		if restored != "" && m.Partial != nil {
			if selected, ok := ilink.ResolvePartialQuote(restored, *m.Partial); ok {
				restored = selected
			} else {
				restored = ""
			}
		}
		if restored != "" {
			out.Quote = joinQuote(out.Quote, restored)
		}
	}
	total := 0
	voiceFiles := 0
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
			if md.Transcribed {
				continue
			}
			blob, n, unsupported := downloadVoiceFallback(ctx, c, md, messageMediaMost-total, voiceFiles)
			if unsupported != "" {
				out.Unsupported = append(out.Unsupported, unsupported)
				continue
			}
			out.Files = append(out.Files, blob)
			total += n
			voiceFiles++
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

func quoteCacheID(account, serverID string) string {
	if strings.TrimSpace(serverID) == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(account))
	return hex.EncodeToString(digest[:]) + "\x00" + serverID
}

func joinQuote(inline, restored string) string {
	switch {
	case restored == "":
		return inline
	case inline == "":
		return restored
	case inline == restored:
		return inline
	default:
		return inline + "\n" + restored
	}
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
