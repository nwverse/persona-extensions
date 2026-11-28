package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestSteamHTTPDefaultTransport(t *testing.T) {
	client := steamHTTP(nil, time.Second)
	transport, ok := client.Transport.(*steamTransport).base.(*http.Transport)
	if !ok || transport == http.DefaultTransport || transport.Proxy == nil || !transport.ForceAttemptHTTP2 {
		t.Fatal("Steam needs its own proxy-aware HTTP/2 transport")
	}
	if client.Timeout != time.Second || transport.TLSHandshakeTimeout <= 0 || transport.ResponseHeaderTimeout <= 0 || transport.IdleConnTimeout <= 0 {
		t.Fatal("client or transport timeouts are missing")
	}
	if h2 := transport.HTTP2; h2 == nil || h2.SendPingTimeout <= 0 || h2.PingTimeout <= 0 || h2.SendPingTimeout+h2.PingTimeout >= httpTimeout {
		t.Fatal("HTTP/2 must detect dead connections within the request timeout")
	}

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	defer client.CloseIdleConnections()
	transport.TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.ProtoMajor != 2 || response.StatusCode != http.StatusNoContent {
		t.Fatalf("unexpected local response: %s %s", response.Proto, response.Status)
	}
}

func TestSteamRequestDeadlinesAllowProxyLatency(t *testing.T) {
	f := newFakeSteam(t, httpTimeout)
	f.reply(reviewsAPIPath, http.StatusOK, `{}`)
	f.reply(storeSearchPath, http.StatusOK, `{}`)
	budgets := map[string]time.Duration{
		reviewsAPIPath:  8 * time.Second,
		storeSearchPath: 5 * time.Second,
		storeBrowsePath: 6 * time.Second,
	}
	transport := f.http.Transport
	f.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		want := budgets[r.URL.Path]
		if left := time.Until(deadline); !ok || left < want-time.Second || left > want {
			t.Errorf("%s has %s remaining, want a deadline near %s", r.URL.Path, left, want)
		}
		return transport.RoundTrip(r)
	})
	c := steamClient{http: f.http}
	for _, target := range []string{apiBase + reviewsAPIPath, storeBase + storeSearchPath} {
		if issue := c.get(t.Context(), target, nil, false, &struct{}{}); issue != "" {
			t.Fatalf("%s: %s", target, issue)
		}
	}
	if items, issue := c.browseStore(t.Context(), []uint32{firstApp}, "US", "english", storeDataRequest{}); issue != "" || len(items) != 1 {
		t.Fatalf("StoreBrowse returned %d items, issue %s", len(items), issue)
	}
}

func TestSteamTransportLimitsActiveBodiesPerOrigin(t *testing.T) {
	f := newFakeSteam(t, 3*time.Second)
	streaming := make(chan struct{}, steamConcurrency+1)
	f.handle(reviewsAPIPath, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "{")
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Error(err)
		}
		streaming <- struct{}{}
		<-r.Context().Done()
	})
	f.reply(storeSearchPath, http.StatusOK, `{}`)
	c := steamClient{http: f.http}
	ctx, cancel := context.WithCancel(t.Context())
	var reads sync.WaitGroup
	defer func() { cancel(); reads.Wait() }()
	for range steamConcurrency {
		reads.Go(func() { c.get(ctx, apiBase+reviewsAPIPath, nil, false, &struct{}{}) })
	}
	for range steamConcurrency {
		select {
		case <-streaming:
		case <-time.After(time.Second):
			t.Fatal("Steam API did not start its allowed concurrent reads")
		}
	}
	if issue := c.get(t.Context(), storeBase+storeSearchPath, nil, false, &struct{}{}); issue != "" {
		t.Fatalf("API streams blocked an independent Store request: %s", issue)
	}
	wait, stop := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer stop()
	if issue := c.get(wait, apiBase+reviewsAPIPath, nil, false, &struct{}{}); issue != problemTimeout || f.hit(reviewsAPIPath) != steamConcurrency {
		t.Fatalf("request queued at the origin exceeded its deadline or opened another stream: %s", issue)
	}
}
