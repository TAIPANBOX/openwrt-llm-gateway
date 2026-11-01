package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestClientRateIsHeldPerMinute(t *testing.T) {
	tg := newTestGateway(t)
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	tg.gw.opts.Now = func() time.Time { return now }
	tg.client.Rate = 2
	other := &Client{Name: "phone", KeyHash: HashKey("llmgw_phone-key-0123456789abcdefghijklmnop"), Models: []string{"*"}}
	tg.gw.cfg.Clients = append(tg.gw.cfg.Clients, other)
	for i := range 2 {
		if w := tg.do(chatRequest("qwen3:14b", testKey)); w.Code != 200 {
			t.Fatalf("request %d within the rate: got %d", i+1, w.Code)
		}
	}
	if w := tg.do(chatRequest("qwen3:14b", testKey)); w.Code != http.StatusTooManyRequests {
		t.Fatalf("third request in the same minute: got %d, want 429", w.Code)
	}
	if n := tg.backend.hits.Load(); n != 2 {
		t.Fatalf("the backend saw %d requests, want 2", n)
	}
	// Another client is not held by this one's rate.
	if w := tg.do(chatRequest("qwen3:14b", "llmgw_phone-key-0123456789abcdefghijklmnop")); w.Code != 200 {
		t.Fatalf("another client: got %d, want 200", w.Code)
	}
	// Half a minute refills one request at a rate of two a minute.
	now = now.Add(30 * time.Second)
	if w := tg.do(chatRequest("qwen3:14b", testKey)); w.Code != 200 {
		t.Fatalf("after half a minute: got %d, want 200", w.Code)
	}
	if w := tg.do(chatRequest("qwen3:14b", testKey)); w.Code != http.StatusTooManyRequests {
		t.Fatalf("second after half a minute: got %d, want 429", w.Code)
	}
}

func TestClientConcurrencyIsHeld(t *testing.T) {
	tg := newTestGateway(t)
	tg.client.Concurrent = 1
	release := make(chan struct{})
	started := make(chan struct{}, 4)
	var first sync.Once
	tg.backend.reply = func(w http.ResponseWriter, r *http.Request, body []byte) {
		// Only the first request is held at the backend; a second one that gets through (the
		// defect this test is for) is answered at once, so the test fails instead of hanging.
		held := false
		first.Do(func() { held = true })
		if held {
			started <- struct{}{}
			<-release
		}
		io.WriteString(w, `{"choices":[]}`)
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		if w := tg.do(chatRequest("qwen3:14b", testKey)); w.Code != 200 {
			t.Errorf("first request: got %d", w.Code)
		}
	})
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("the first request never reached the backend")
	}
	if w := tg.do(chatRequest("qwen3:14b", testKey)); w.Code != http.StatusTooManyRequests {
		t.Fatalf("second request while the first is in flight: got %d, want 429", w.Code)
	}
	close(release)
	wg.Wait()
	if w := tg.do(chatRequest("qwen3:14b", testKey)); w.Code != 200 {
		t.Fatalf("after the first finished: got %d, want 200", w.Code)
	}
	// A refused request is journalled with its reason.
	if !strings.Contains(tg.journal.String(), `"reason":"concurrency"`) {
		t.Fatalf("journal lacks the concurrency refusal:\n%s", tg.journal.String())
	}
	_ = httptest.NewRecorder
}
