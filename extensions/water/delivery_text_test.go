package main

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The page shows plain Chinese for every delivery outcome: no hub codes and no status numbers.
func TestDeliveryOutcomesShownAsPlainChinese(t *testing.T) {
	for _, tc := range []struct {
		name, status, body, want, raw string
		seconds                       []int // when each attempt happens, relative to the first
	}{
		{"no device", "200", `{"sent":false,"reason":"no_device"}`, "暂时没有能收到通知的设备", "no_device", []int{0}},
		{"filtered", "200", `{"sent":false,"reason":"filtered"}`, "设备的通知设置挡住了这条提醒", "filtered", []int{0}},
		{"muted", "200", `{"sent":false,"reason":"muted"}`, "通知被静音了", "muted", []int{0}},
		{"unknown reason", "200", `{"sent":false,"reason":"quota_exceeded"}`, "提醒没有送达", "quota_exceeded", []int{0}},
		{"server error after retries", "503", "unavailable", "暂时连不上提醒服务，稍后会再试", "503", []int{0, 30, 60}},
		{"client error", "403", "denied", "这次提醒没有发出去", "403", []int{0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := fakeServer(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/v1/push/ln-mode":
					io.WriteString(w, `{"enabled":false}`)
				case r.Method == http.MethodPost && r.URL.Path == "/api/v1/notify":
					code, _ := strconv.Atoi(tc.status)
					w.WriteHeader(code)
					io.WriteString(w, tc.body)
				default:
					http.NotFound(w, r)
				}
			})
			defer srv.Close()
			base := instant("2026-11-16T10:00:00Z")
			a := testApp(t, defaults(), newHub(srv.URL, "test-token", []string{"notify"}), base)
			for _, sec := range tc.seconds {
				at := base.Add(time.Duration(sec) * time.Second)
				if err := tickAt(a, context.Background(), at); err != nil {
					t.Fatal(err)
				}
			}
			v, err := a.render(context.Background(), request("", "", nil))
			if err != nil {
				t.Fatal(err)
			}
			var shown []string
			for _, item := range v.Sections[0].Items {
				if item.Type == "metric" && item.Tone == "caution" {
					shown = append(shown, item.Value)
				}
			}
			if len(shown) != 1 || shown[0] != tc.want {
				t.Fatalf("cautions %q, want exactly %q", shown, tc.want)
			}
			if strings.Contains(shown[0], tc.raw) || strings.ContainsAny(shown[0], "0123456789") {
				t.Fatalf("technical text on the page: %q", shown[0])
			}
		})
	}
}
