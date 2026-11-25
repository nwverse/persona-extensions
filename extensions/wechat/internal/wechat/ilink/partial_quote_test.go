package ilink

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestNestedMessageAndReferenceIDs(t *testing.T) {
	var original wireMessage
	if err := json.Unmarshal([]byte(`{"seq":9,"from_user_id":"owner@im","context_token":"CT","item_list":[{"type":1,"msg_id":"nested-server-id","text_item":{"text":"original"}}]}`), &original); err != nil {
		t.Fatal(err)
	}
	decoded, ok := decode(original)
	if !ok || decoded.SvrID != "nested-server-id" || decoded.ID != "message:nested-server-id" {
		t.Fatalf("nested message id = %+v, %v", decoded, ok)
	}
	var quoted wireMessage
	if err := json.Unmarshal([]byte(`{"message_id":10,"from_user_id":"owner@im","context_token":"CT","item_list":[{"type":1,"text_item":{"text":"reply"},"ref_msg":{"message_item":{"type":1,"msg_id":"nested-reference-id"}}}]}`), &quoted); err != nil {
		t.Fatal(err)
	}
	decoded, ok = decode(quoted)
	if !ok || decoded.QuoteID != "nested-reference-id" {
		t.Fatalf("nested reference id = %+v, %v", decoded, ok)
	}
}

func TestNumericNestedIDsSurviveGetUpdates(t *testing.T) {
	const largeID = uint64(18446744073709551615)
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		writeJSON(w, map[string]any{"ret": 0, "get_updates_buf": "C1", "msgs": []any{
			map[string]any{"seq": 9, "from_user_id": "owner@im", "context_token": "CT", "item_list": []any{
				map[string]any{"type": itemText, "msg_id": largeID, "text_item": map[string]any{"text": "original"}},
			}},
			map[string]any{"message_id": 10, "from_user_id": "owner@im", "context_token": "CT", "item_list": []any{
				map[string]any{"type": itemText, "text_item": map[string]any{"text": "reply"}, "ref_msg": map[string]any{
					"message_item": map[string]any{"type": itemText, "msg_id": largeID},
				}},
			}},
		}})
	})
	p, err := f.client("BT", "").GetUpdates(context.Background(), "C0")
	if err != nil || len(p.Messages) != 2 {
		t.Fatalf("poll = %+v, %v", p, err)
	}
	if got := p.Messages[0].SvrID; got != "18446744073709551615" {
		t.Fatalf("numeric nested message id = %q", got)
	}
	if got := p.Messages[1].QuoteID; got != "18446744073709551615" {
		t.Fatalf("numeric nested reference id = %q", got)
	}
}

func TestResolvePartialQuote(t *testing.T) {
	partial := PartialQuote{Start: "B", End: "C", StartIndex: 1, EndIndex: 1}
	if got, ok := ResolvePartialQuote("A B C A B C", partial); !ok || got != "B C" {
		t.Fatalf("relative selection = %q, %v", got, ok)
	}
	partial = PartialQuote{Start: "B", End: "C", StartIndex: 0, EndIndex: 1, QuoteMD5: "incorrect"}
	if _, ok := ResolvePartialQuote("A B C A B C", partial); ok {
		t.Fatal("hash mismatch must not restore an arbitrary range")
	}
}

func TestDecodePartialText(t *testing.T) {
	var message wireMessage
	if err := json.Unmarshal([]byte(`{"message_id":11,"from_user_id":"owner@im","context_token":"CT","item_list":[{"type":1,"text_item":{"text":"reply"},"ref_msg":{"svr_id":"42","partial_text":{"start":"B","end":"C","startindex":1,"endindex":1,"quotemd5":"hash"}}}]}`), &message); err != nil {
		t.Fatal(err)
	}
	decoded, ok := decode(message)
	if !ok || decoded.Partial == nil || decoded.Partial.Start != "B" || decoded.Partial.End != "C" || decoded.Partial.StartIndex != 1 || decoded.Partial.EndIndex != 1 || decoded.Partial.QuoteMD5 != "hash" {
		t.Fatalf("partial text = %+v, %v", decoded.Partial, ok)
	}
}
