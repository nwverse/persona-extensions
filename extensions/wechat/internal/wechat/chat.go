package wechat

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
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

// send sends text to peer as the bot: WeChat's subset of Markdown, in pieces it takes. ref, when given, is the hub's id
// for the message, which each piece's client_id comes from (sendIDs); without one, each piece gets a random id.
func (b *Bot) send(ctx context.Context, c *ilink.Client, peer, contextToken, replyTo, ref, text string) bool {
	if c == nil || contextToken == "" {
		log.Printf("wechat: nothing to send with (no client or context token)")
		return false
	}
	parts := splitText(text)
	ids := newSendIDs(ref, parts, nil)
	for i, part := range parts {
		if !b.sendPiece(ctx, c, peer, contextToken, replyTo, ref, partOf(i, len(parts)), ids.parts[i], part) {
			return false
		}
	}
	return true
}

// splitText is the pieces of text that WeChat takes, one message each.
func splitText(text string) []string {
	return ilink.Chunk(ilink.FilterMarkdown(strings.TrimSpace(text)), 0)
}

// partOf is how the log names text piece i of n.
func partOf(i, n int) string { return fmt.Sprintf("part=%d/%d", i+1, n) }

// sendTries is how many times a piece or a picture is tried while its failure may pass (ilink.Retryable).
const sendTries = 3

// sendRetryWait is the wait before the second try, and twice it before the third; tests shorten it.
var sendRetryWait = time.Second

// retryWait waits between tries; tests count it.
var retryWait = sleep

// retry calls try until it succeeds, fails in a way that cannot pass, or was called sendTries times, waiting
// sendRetryWait and then twice that between calls; no wait follows the last call, so its error is what retry answers.
// Cancelled while waiting, retry answers the cancellation.
func retry(ctx context.Context, try func() error) error {
	for attempt := 1; ; attempt++ {
		err := try()
		if err == nil || !ilink.Retryable(err) || attempt == sendTries {
			return err
		}
		if !retryWait(ctx, time.Duration(attempt)*sendRetryWait) {
			return ctx.Err()
		}
	}
}

// sendPiece sends one text piece with id as its client_id, trying a failure that may pass again. Its final result is
// logged with the inbound message, ref, what (its place: part=i/n, or note) and duration, never the text; a piece WeChat
// confirmed goes into the quote index.
func (b *Bot) sendPiece(ctx context.Context, c *ilink.Client, peer, contextToken, replyTo, ref, what, id, text string) bool {
	started := time.Now()
	var serverID string
	err := retry(ctx, func() (err error) {
		serverID, err = c.SendTextWithID(ctx, peer, contextToken, id, text)
		return err
	})
	took := time.Since(started)
	if err != nil {
		log.Printf("wechat: send reply_to=%q ref=%q %s result=failure duration=%s error_type=%s error=%v",
			replyTo, ref, what, took, ilink.ErrorType(err), err)
		return false
	}
	log.Printf("wechat: send reply_to=%q ref=%q %s result=success duration=%s", replyTo, ref, what, took)
	b.rememberOutbound(peer, serverID, text, nil)
	return true
}

// rememberOutbound keeps what the bot sent the owner in the quote index by its server message id (text, or a
// picture's summary), so that the owner quoting it is restored like a quote of their own message.
func (b *Bot) rememberOutbound(peer, serverID, text string, media []quoteMedia) {
	if b.quotes == nil || serverID == "" {
		return
	}
	b.mu.Lock()
	owner := b.st.Creds != nil && b.st.Creds.UserID == peer
	b.mu.Unlock()
	if owner {
		b.quotes.put(quoteCacheID(peer, serverID), text, media)
	}
}

// Send is the hub's channel.send: text to the owner, in the chat of their latest message, then its pictures. Before
// the owner has written to the bot there is no context token, and nothing can be sent: errCannotSend.
//
// A picture that cannot be sent (not a picture, too large, or refused by WeChat in a way that does not pass) is skipped
// and the pictures after it go on; a last text then tells the owner how many were not sent. A failure that may pass
// stops the send (sendFailed): the hub sends the same ref again, and what WeChat confirmed of it is remembered by ref
// (sendProgress), so the next send of the ref goes on from the piece or picture that failed.
func (b *Bot) Send(ctx context.Context, p wire.ChannelSendParams) (wire.ChannelSendResult, error) {
	b.mu.Lock()
	c, creds, contextToken := b.client, b.st.Creds, b.st.Context
	b.mu.Unlock()
	if creds == nil || c == nil || contextToken == "" {
		return wire.ChannelSendResult{}, errCannotSend
	}
	peer, parts := creds.UserID, splitText(p.Text)
	ids := newSendIDs(p.Ref, parts, p.Images)
	pr := b.resume(p.Ref, ids)
	if textDone, imagesDone := countIn(pr.done, ids.parts), countIn(pr.done, ids.images); textDone > 0 || imagesDone > 0 {
		log.Printf("wechat: send reply_to=%q ref=%q resume text=%d/%d image=%d/%d",
			p.ReplyTo, p.Ref, textDone, len(parts), imagesDone, len(p.Images))
	}
	failed := func() (wire.ChannelSendResult, error) {
		b.keep(p.Ref, pr)
		return wire.ChannelSendResult{}, sendFailed{b.lang()}
	}
	for i, part := range parts {
		id := ids.parts[i]
		if pr.done[id] {
			continue
		}
		if !b.sendPiece(ctx, c, peer, contextToken, p.ReplyTo, p.Ref, partOf(i, len(parts)), id, part) {
			return failed()
		}
		pr.done[id] = true
	}
	for i, img := range p.Images {
		id := ids.images[i]
		if pr.done[id] {
			continue
		}
		up := pr.uploaded(id)
		switch b.sendImage(ctx, c, peer, contextToken, p.ReplyTo, p.Ref, id, i, len(p.Images), img, &up) {
		case imageFailed:
			pr.upload, pr.uploadOf = up, id
			return failed()
		case imageSkipped:
			pr.skipped[id] = true
		}
		pr.done[id] = true
	}
	if n := countIn(pr.skipped, ids.images); n > 0 {
		note := t(b.lang(), "chat.images_not_sent", "count", strconv.Itoa(n))
		if !b.sendPiece(ctx, c, peer, contextToken, p.ReplyTo, p.Ref, "note", ids.note(note), note) {
			return failed()
		}
	}
	b.forget(p.Ref)
	return wire.ChannelSendResult{}, nil
}

// sendIDs are the client_ids of one send's text pieces and pictures. With a ref, each comes from the ref, the piece's
// kind and place, and its content: the hub's retry of the same message has the same ids (iLink may keep a repeat once,
// and the progress kept by ref knows them), and other content under the same ref has other ids, so none of it is
// taken for what was sent before. Without a ref they are random.
type sendIDs struct {
	ref           string
	parts, images []string
}

func newSendIDs(ref string, parts []string, images []wire.ChannelBlob) sendIDs {
	ids := sendIDs{ref: ref}
	for i, part := range parts {
		ids.parts = append(ids.parts, contentID(ref, "text", i, part))
	}
	for i, img := range images {
		ids.images = append(ids.images, contentID(ref, "image", i, img.Data))
	}
	return ids
}

// note is the client_id of the text that says how many pictures were not sent: from the ref, the send's other ids and
// the text.
func (s sendIDs) note(text string) string {
	return contentID(s.ref, "note", 0, strings.Join(s.parts, ",")+"|"+strings.Join(s.images, ",")+"|"+text)
}

// contentID is the client_id of a piece of kind at place i with content, under ref; random without a ref.
func contentID(ref, kind string, i int, content string) string {
	if ref == "" {
		var r [12]byte
		_, _ = rand.Read(r[:])
		return "persona-" + hex.EncodeToString(r[:])
	}
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%d:%s|%s|%d|", len(ref), ref, kind, i)
	_, _ = io.WriteString(h, content)
	return "persona-" + hex.EncodeToString(h.Sum(nil)[:12])
}

// progressMost is how many refs' progress the bot keeps; past it the oldest is dropped (its send then starts over).
const progressMost = 64

// sendProgress is how far a ref's channel.send got, by client_id (sendIDs).
type sendProgress struct {
	// done are the text pieces and pictures WeChat confirmed, and the pictures skipped: none is sent again.
	done map[string]bool
	// skipped are the pictures skipped as unsendable, for the text that says how many were not sent.
	skipped map[string]bool
	// upload is picture uploadOf on WeChat's CDN, which a try uploaded but did not send: the next try only sends it.
	upload   ilink.Uploaded
	uploadOf string
}

// uploaded is picture id's upload kept from an earlier try; the zero Uploaded when there is none.
func (pr sendProgress) uploaded(id string) ilink.Uploaded {
	if pr.uploadOf == id {
		return pr.upload
	}
	return ilink.Uploaded{}
}

// countIn is how many of ids are in set.
func countIn(set map[string]bool, ids []string) int {
	n := 0
	for _, id := range ids {
		if set[id] {
			n++
		}
	}
	return n
}

// resume is what is kept of ref's send, for the pieces and pictures in ids only: a ref sent again with other content
// goes on with what WeChat does not have of it. Nothing is kept without a ref.
func (b *Bot) resume(ref string, ids sendIDs) sendProgress {
	pr := sendProgress{done: map[string]bool{}, skipped: map[string]bool{}}
	if ref == "" {
		return pr
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	kept, ok := b.progress[ref]
	if !ok {
		return pr
	}
	for _, id := range slices.Concat(ids.parts, ids.images) {
		if kept.done[id] {
			pr.done[id] = true
		}
		if kept.skipped[id] {
			pr.skipped[id] = true
		}
	}
	if slices.Contains(ids.images, kept.uploadOf) {
		pr.upload, pr.uploadOf = kept.upload, kept.uploadOf
	}
	return pr
}

// keep records pr as how far ref's send got: it failed, and the hub sends the ref again.
func (b *Bot) keep(ref string, pr sendProgress) {
	if ref == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.progress == nil {
		b.progress = map[string]sendProgress{}
	}
	if _, ok := b.progress[ref]; !ok {
		b.progressOrder = append(b.progressOrder, ref)
		for len(b.progressOrder) > progressMost {
			delete(b.progress, b.progressOrder[0])
			b.progressOrder = b.progressOrder[1:]
		}
	}
	b.progress[ref] = pr
}

// forget drops ref's progress: its send is done (sentRefs remembers that from here on).
func (b *Bot) forget(ref string) {
	if ref == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.progress, ref)
	b.progressOrder = slices.DeleteFunc(b.progressOrder, func(r string) bool { return r == ref })
}

// forgetSendsLocked drops every ref's progress, b.mu held: what it says was sent went to a WeChat no longer bound.
func (b *Bot) forgetSendsLocked() {
	b.progress, b.progressOrder = nil, nil
}

// imageResult is how sending one picture ended.
type imageResult int

const (
	imageSent imageResult = iota
	// imageSkipped: the picture cannot be sent (not a picture, too large, or refused by WeChat in a way that does not
	// pass); the pictures after it go on.
	imageSkipped
	// imageFailed: a failure that may pass remains after the picture's tries; the send stops at it, and the hub's next
	// send of the ref goes on from it.
	imageFailed
)

// sendImage uploads one of the hub's pictures (the i-th of n) to WeChat's CDN and sends it with id as its client_id,
// trying a failure that may pass again (retry). *upload is the picture once the CDN took it (zero before): a try after
// that only sends it, and Send keeps it for the hub's next send of the ref. Its final result is logged like a text
// piece's, with image=i/n in place of part=i/n (never the picture, its upload URL or keys); a picture WeChat confirmed
// goes into the quote index.
func (b *Bot) sendImage(ctx context.Context, c *ilink.Client, peer, contextToken, replyTo, ref, id string, i, n int, img wire.ChannelBlob, upload *ilink.Uploaded) imageResult {
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
		return imageSkipped
	}
	var serverID string
	err = retry(ctx, func() (err error) {
		if upload.Query == "" {
			if *upload, err = c.UploadImage(ctx, peer, data); err != nil {
				return err
			}
		}
		serverID, err = c.SendImage(ctx, peer, contextToken, id, *upload)
		return err
	})
	took := time.Since(started)
	switch {
	case err == nil:
		log.Printf("%s result=success duration=%s", head, took)
		b.rememberOutbound(peer, serverID, "", []quoteMedia{{Kind: string(ilink.MediaImage)}})
		return imageSent
	case !ilink.Retryable(err):
		log.Printf("%s result=skipped reason=refused duration=%s error_type=%s error=%v", head, took, ilink.ErrorType(err), err)
		return imageSkipped
	}
	log.Printf("%s result=failure duration=%s error_type=%s error=%v", head, took, ilink.ErrorType(err), err)
	return imageFailed
}

// sendFailed: WeChat did not take the message (logged by the hub, which sends it again with the same ref).
type sendFailed struct{ lang string }

func (e sendFailed) Error() string { return t(e.lang, "chat.send_failed") }

// empty says whether m has nothing to hand on.
func empty(m wire.ChannelMessage) bool {
	return strings.TrimSpace(m.Text) == "" && len(m.Images) == 0 && len(m.Files) == 0 && m.Quote == "" && m.Forward == "" &&
		len(m.Unsupported) == 0
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
