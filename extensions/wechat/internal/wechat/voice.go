package wechat

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/nwverse/persona-extensions/extensions/wechat/internal/wechat/ilink"
	"github.com/nwverse/persona/sdk/go/wire"
)

const (
	// defaultVoiceSampleRate matches WeChat/openclaw-weixin voice audio (24 kHz).
	defaultVoiceSampleRate = 24000
	minVoiceSampleRate     = 8000
	maxVoiceSampleRate     = 192000
	wavHeaderBytes         = 44
)

var (
	errVoiceEmpty         = errors.New("voice payload is empty")
	errVoiceInvalidFormat = errors.New("unsupported or invalid voice format")
	errVoiceTooLarge      = errors.New("voice payload exceeds size limit")
)

// downloadVoiceFallback downloads and decrypts an untranscribed voice item into a file attachment.
// When the audio cannot be fetched, decrypted, or validated within limits, it returns an assistant-readable
// unsupported reason instead of dropping the voice message silently.
func downloadVoiceFallback(ctx context.Context, c *ilink.Client, md ilink.Media, remaining, voiceIdx int) (wire.ChannelBlob, int, string) {
	if md.URL == "" && md.Query == "" {
		return wire.ChannelBlob{}, 0, "一段没有转写的语音" // i18n-ignore: what the assistant reads
	}
	if remaining <= 0 || md.Size > int64(remaining) {
		return wire.ChannelBlob{}, 0, "一段太大的语音" // i18n-ignore: what the assistant reads
	}
	if c == nil {
		return wire.ChannelBlob{}, 0, "一段没有转写的语音" // i18n-ignore: what the assistant reads
	}
	data, err := c.Download(ctx, md)
	if err != nil {
		log.Printf("wechat: downloading a voice message: %v", err)
		if errors.Is(err, ilink.ErrMediaTooLarge) {
			return wire.ChannelBlob{}, 0, "一段太大的语音" // i18n-ignore: what the assistant reads
		}
		return wire.ChannelBlob{}, 0, "一段没下载下来的语音" // i18n-ignore: what the assistant reads
	}
	ext, mime, payload, err := prepareVoiceAudio(md, data)
	if err != nil {
		log.Printf("wechat: preparing a voice message: %v", err)
		switch {
		case errors.Is(err, errVoiceTooLarge):
			return wire.ChannelBlob{}, 0, "一段太大的语音" // i18n-ignore: what the assistant reads
		case errors.Is(err, errVoiceEmpty):
			return wire.ChannelBlob{}, 0, "一段没下载下来的语音" // i18n-ignore: what the assistant reads
		default:
			return wire.ChannelBlob{}, 0, "一段无法处理的语音" // i18n-ignore: what the assistant reads
		}
	}
	if len(payload) > ilink.MaxMedia || len(payload) > remaining {
		return wire.ChannelBlob{}, 0, "一段太大的语音" // i18n-ignore: what the assistant reads
	}
	return wire.ChannelBlob{
		Name: voiceFileName(voiceIdx, ext),
		Mime: mime,
		Data: base64.StdEncoding.EncodeToString(payload),
	}, len(payload), ""
}

func voiceFileName(index int, ext string) string {
	if index <= 0 {
		return "voice" + ext
	}
	return "voice-" + strconv.Itoa(index+1) + ext
}

// prepareVoiceAudio determines the audio container/MIME from decrypted magic bytes and iLink's
// voice encode_type, wrapping raw PCM (encode_type=1) in a standard mono WAV header without
// adding external transcoding dependencies.
func prepareVoiceAudio(md ilink.Media, data []byte) (ext, mime string, payload []byte, err error) {
	if len(data) == 0 {
		return "", "", nil, errVoiceEmpty
	}
	switch {
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WAVE":
		return ".wav", "audio/wav", data, nil
	case bytes.HasPrefix(data, []byte("\x02#!SILK_V3")) || bytes.HasPrefix(data, []byte("#!SILK_V3")):
		return ".silk", "audio/silk", data, nil
	case bytes.HasPrefix(data, []byte("#!AMR\n")) || bytes.HasPrefix(data, []byte("#!AMR-WB\n")):
		return ".amr", "audio/amr", data, nil
	case bytes.HasPrefix(data, []byte("OggS")):
		return ".ogg", "audio/ogg", data, nil
	case bytes.HasPrefix(data, []byte("ID3")):
		return ".mp3", "audio/mpeg", data, nil
	case len(data) >= 8 && string(data[4:8]) == "ftyp":
		return ".m4a", "audio/mp4", data, nil
	case (md.EncodeType == 0 || md.EncodeType == ilink.VoiceEncodeMP3) && isMP3FrameSync(data):
		return ".mp3", "audio/mpeg", data, nil
	}
	switch md.EncodeType {
	case ilink.VoiceEncodePCM:
		wav, err := pcmToWAV(data, md.SampleRate, md.BitsPerSample)
		if err != nil {
			return "", "", nil, err
		}
		return ".wav", "audio/wav", wav, nil
	case ilink.VoiceEncodeADPCM:
		return ".adpcm", "audio/adpcm", data, nil
	case ilink.VoiceEncodeSpeex:
		return ".spx", "audio/speex", data, nil
	case ilink.VoiceEncodeAMR:
		return ".amr", "audio/amr", data, nil
	case ilink.VoiceEncodeSILK:
		return ".silk", "audio/silk", data, nil
	case ilink.VoiceEncodeMP3:
		return ".mp3", "audio/mpeg", data, nil
	case ilink.VoiceEncodeOGGSpeex:
		return ".ogg", "audio/ogg", data, nil
	case 0:
		switch http.DetectContentType(data) {
		case "audio/wave", "audio/wav":
			return ".wav", "audio/wav", data, nil
		case "audio/mpeg":
			return ".mp3", "audio/mpeg", data, nil
		case "audio/ogg":
			return ".ogg", "audio/ogg", data, nil
		case "audio/aiff":
			return ".aiff", "audio/aiff", data, nil
		case "audio/basic":
			return ".au", "audio/basic", data, nil
		default:
			// Match openclaw-weixin's raw SILK fallback when encode_type is omitted.
			return ".silk", "audio/silk", data, nil
		}
	default:
		return "", "", nil, fmt.Errorf("%w: encode_type=%d", errVoiceInvalidFormat, md.EncodeType)
	}
}

func isMP3FrameSync(data []byte) bool {
	if len(data) < 4 {
		return false
	}
	return data[0] == 0xff && (data[1]&0xe0) == 0xe0 &&
		(data[1]&0x18) != 0x08 && (data[1]&0x06) != 0x00 &&
		(data[2]&0xf0) != 0xf0 && (data[2]&0x0c) != 0x0c
}

// pcmToWAV wraps mono little-endian PCM bytes in a 44-byte RIFF/WAVE container.
func pcmToWAV(pcm []byte, sampleRate, bitsPerSample int) ([]byte, error) {
	if len(pcm) == 0 {
		return nil, errVoiceEmpty
	}
	if sampleRate == 0 {
		sampleRate = defaultVoiceSampleRate
	}
	if sampleRate < minVoiceSampleRate || sampleRate > maxVoiceSampleRate {
		return nil, fmt.Errorf("%w: sample_rate=%d", errVoiceInvalidFormat, sampleRate)
	}
	if bitsPerSample == 0 {
		bitsPerSample = 16
	}
	if bitsPerSample != 8 && bitsPerSample != 16 {
		return nil, fmt.Errorf("%w: bits_per_sample=%d", errVoiceInvalidFormat, bitsPerSample)
	}
	bytesPerSample := bitsPerSample / 8
	if len(pcm)%bytesPerSample != 0 {
		return nil, fmt.Errorf("%w: unaligned pcm length", errVoiceInvalidFormat)
	}
	if len(pcm) > ilink.MaxMedia-wavHeaderBytes {
		return nil, errVoiceTooLarge
	}
	out := make([]byte, wavHeaderBytes+len(pcm))
	copy(out[0:4], "RIFF")
	binary.LittleEndian.PutUint32(out[4:8], uint32(36+len(pcm)))
	copy(out[8:12], "WAVE")
	copy(out[12:16], "fmt ")
	binary.LittleEndian.PutUint32(out[16:20], 16)
	binary.LittleEndian.PutUint16(out[20:22], 1) // PCM
	binary.LittleEndian.PutUint16(out[22:24], 1) // mono
	binary.LittleEndian.PutUint32(out[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(out[28:32], uint32(sampleRate*bytesPerSample))
	binary.LittleEndian.PutUint16(out[32:34], uint16(bytesPerSample))
	binary.LittleEndian.PutUint16(out[34:36], uint16(bitsPerSample))
	copy(out[36:40], "data")
	binary.LittleEndian.PutUint32(out[40:44], uint32(len(pcm)))
	copy(out[wavHeaderBytes:], pcm)
	return out, nil
}
