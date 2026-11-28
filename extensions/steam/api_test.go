package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSteamHTTPDefaultTransport(t *testing.T) {
	client := steamHTTP(nil, time.Second)
	transport, ok := client.Transport.(*http.Transport)
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
