package ilink

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Wire constants (openclaw-weixin src/api/types.ts).
const (
	typeUser = 1
	typeBot  = 2

	stateFinish = 2

	itemText  = 1
	itemImage = 2
	itemVoice = 3
	itemFile  = 4
	itemVideo = 5

	typingOn     = 1
	typingCancel = 2
)

// Voice encode_type values (openclaw-weixin src/api/types.ts).
const (
	VoiceEncodePCM      = 1
	VoiceEncodeADPCM    = 2
	VoiceEncodeFeature  = 3
	VoiceEncodeSpeex    = 4
	VoiceEncodeAMR      = 5
	VoiceEncodeSILK     = 6
	VoiceEncodeMP3      = 7
	VoiceEncodeOGGSpeex = 8
)

type wireMedia struct {
	EncryptQueryParam string `json:"encrypt_query_param"`
	AESKey            string `json:"aes_key"`
	FullURL           string `json:"full_url"`
}

type wirePartialText struct {
	Start      string `json:"start"`
	End        string `json:"end"`
	StartIndex int    `json:"startindex"`
	EndIndex   int    `json:"endindex"`
	QuoteMD5   string `json:"quotemd5"`
}

type wireItem struct {
	Type   int             `json:"type"`
	MsgID  json.RawMessage `json:"msg_id,omitempty"`
	RefMsg *struct {
		Title       string           `json:"title"`
		MessageItem *wireItem        `json:"message_item"`
		SvrID       json.RawMessage  `json:"svr_id"`
		PartialText *wirePartialText `json:"partial_text"`
	} `json:"ref_msg"`
	TextItem *struct {
		Text string `json:"text"`
	} `json:"text_item"`
	ImageItem *struct {
		Media  *wireMedia `json:"media"`
		AESKey string     `json:"aeskey"`
	} `json:"image_item"`
	VoiceItem *struct {
		Media         *wireMedia `json:"media"`
		EncodeType    int        `json:"encode_type"`
		BitsPerSample int        `json:"bits_per_sample"`
		SampleRate    int        `json:"sample_rate"`
		PlayTime      int        `json:"playtime"`
		Text          string     `json:"text"`
	} `json:"voice_item"`
	FileItem *struct {
		Media    *wireMedia `json:"media"`
		FileName string     `json:"file_name"`
		Len      string     `json:"len"`
	} `json:"file_item"`
	VideoItem *struct {
		Media *wireMedia `json:"media"`
	} `json:"video_item"`
}

type wireMessage struct {
	Seq          *int64          `json:"seq"`
	MessageID    json.RawMessage `json:"message_id"`
	FromUserID   string          `json:"from_user_id"`
	ToUserID     string          `json:"to_user_id"`
	ClientID     string          `json:"client_id"`
	CreateTimeMS int64           `json:"create_time_ms"`
	MessageType  *int            `json:"message_type"`
	Items        []wireItem      `json:"item_list"`
	ContextToken string          `json:"context_token"`
}

// MediaKind is what a Media is.
type MediaKind string

const (
	MediaImage MediaKind = "image"
	MediaVoice MediaKind = "voice"
	MediaFile  MediaKind = "file"
	MediaVideo MediaKind = "video"
)

// Media is a reference to an encrypted file on WeChat's CDN; Download fetches and decrypts it.
type Media struct {
	Kind          MediaKind
	URL           string
	Query         string
	KeyB64        string
	KeyHex        string
	FileName      string
	Size          int64
	Transcribed   bool
	EncodeType    int
	SampleRate    int
	BitsPerSample int
	PlayTime      int
}

type PartialQuote struct {
	Start      string
	End        string
	StartIndex int
	EndIndex   int
	QuoteMD5   string
}

// Message is a message a user sent the bot.
type Message struct {
	// ID is stable across redeliveries: the client id, else the message id, else the sequence number.
	ID string
	// SvrID is the exact server message id, used by newer clients to refer to this message.
	SvrID        string
	From         string
	ContextToken string
	At           int64 // milliseconds
	// Text joins the text items; a voice message contributes WeChat's own transcript.
	Text  string
	Media []Media
	// Quote is the message this one replies to, as text (its title and text), "" when none.
	Quote string
	// QuoteID is the referenced server id when the client omitted the quoted body.
	QuoteID string
	// Partial describes the selected substring of the quoted text, when WeChat supplies one.
	Partial *PartialQuote
}

func decode(m wireMessage) (Message, bool) {
	if m.MessageType != nil && *m.MessageType != typeUser {
		return Message{}, false
	}
	svrID := messageSvrID(m)
	out := Message{ID: messageID(m), SvrID: svrID, From: strings.TrimSpace(m.FromUserID), ContextToken: strings.TrimSpace(m.ContextToken), At: m.CreateTimeMS}
	if out.ID == "" || out.From == "" || out.ContextToken == "" {
		return Message{}, false
	}
	var text []string
	for _, it := range m.Items {
		if out.Quote == "" && out.QuoteID == "" && it.RefMsg != nil {
			out.Quote = quote(it)
			if strings.TrimSpace(quoteBody(it.RefMsg.MessageItem)) == "" {
				out.QuoteID = serverMessageID(it.RefMsg.SvrID)
				if out.QuoteID == "" && it.RefMsg.MessageItem != nil {
					out.QuoteID = serverMessageID(it.RefMsg.MessageItem.MsgID)
				}
			}
			if p := partialQuote(it.RefMsg.PartialText); p != nil {
				out.Partial = p
			}
		}
		switch it.Type {
		case itemText:
			if it.TextItem != nil && it.TextItem.Text != "" {
				text = append(text, it.TextItem.Text)
			}
		case itemVoice:
			if it.VoiceItem != nil && strings.TrimSpace(it.VoiceItem.Text) != "" {
				text = append(text, it.VoiceItem.Text)
			}
		}
		if md, ok := media(it); ok {
			out.Media = append(out.Media, md)
		}
	}
	out.Text = strings.Join(text, "\n")
	return out, true
}

func messageID(m wireMessage) string {
	if s := strings.TrimSpace(m.ClientID); s != "" {
		return "client:" + s
	}
	if id := messageSvrID(m); id != "" {
		return "message:" + id
	}
	if m.Seq != nil {
		return "seq:" + strconv.FormatInt(*m.Seq, 10)
	}
	return ""
}

func messageSvrID(m wireMessage) string {
	if id := serverMessageID(m.MessageID); id != "" {
		return id
	}
	for _, it := range m.Items {
		if id := serverMessageID(it.MsgID); id != "" {
			return id
		}
	}
	return ""
}

func serverMessageID(raw json.RawMessage) string {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	if n, err := strconv.ParseInt(text, 10, 64); err == nil {
		return strconv.FormatInt(n, 10)
	}
	if _, err := strconv.ParseFloat(text, 64); err == nil {
		return text
	}
	return ""
}

func quote(it wireItem) string {
	r := it.RefMsg
	var parts []string
	if t := strings.TrimSpace(r.Title); t != "" {
		parts = append(parts, t)
	}
	if body := quoteBody(r.MessageItem); body != "" {
		parts = append(parts, body)
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func quoteBody(q *wireItem) string {
	if q == nil {
		return ""
	}
	switch {
	case q.Type == itemText && q.TextItem != nil:
		return q.TextItem.Text
	case q.Type == itemVoice && q.VoiceItem != nil:
		return q.VoiceItem.Text
	}
	return ""
}

func partialQuote(p *wirePartialText) *PartialQuote {
	if p == nil {
		return nil
	}
	return &PartialQuote{Start: p.Start, End: p.End, StartIndex: p.StartIndex, EndIndex: p.EndIndex, QuoteMD5: p.QuoteMD5}
}

func media(it wireItem) (Media, bool) {
	ref := func(k MediaKind, w *wireMedia) Media {
		m := Media{Kind: k}
		if w != nil {
			m.URL, m.Query, m.KeyB64 = w.FullURL, w.EncryptQueryParam, w.AESKey
		}
		return m
	}
	switch {
	case it.Type == itemImage && it.ImageItem != nil:
		m := ref(MediaImage, it.ImageItem.Media)
		m.KeyHex = it.ImageItem.AESKey
		return m, true
	case it.Type == itemVoice && it.VoiceItem != nil:
		m := ref(MediaVoice, it.VoiceItem.Media)
		m.Transcribed = strings.TrimSpace(it.VoiceItem.Text) != ""
		m.EncodeType = it.VoiceItem.EncodeType
		m.SampleRate = it.VoiceItem.SampleRate
		m.BitsPerSample = it.VoiceItem.BitsPerSample
		m.PlayTime = it.VoiceItem.PlayTime
		return m, true
	case it.Type == itemFile && it.FileItem != nil:
		m := ref(MediaFile, it.FileItem.Media)
		m.FileName = it.FileItem.FileName
		m.Size, _ = strconv.ParseInt(strings.TrimSpace(it.FileItem.Len), 10, 64)
		return m, true
	case it.Type == itemVideo && it.VideoItem != nil:
		return ref(MediaVideo, it.VideoItem.Media), true
	}
	return Media{}, false
}
