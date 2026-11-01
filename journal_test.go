package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	promptMarker     = "PROMPT-MARKER-7f3c"
	completionMarker = "COMPLETION-MARKER-9a1d"
)

func TestJournalRecordsTheRequestWithoutContent(t *testing.T) {
	tg := newTestGateway(t)
	tg.backend.reply = func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"`+completionMarker+`"}}],"usage":{"prompt_tokens":120,"completion_tokens":40}}`)
	}
	body := `{"model":"qwen3:14b","messages":[{"role":"user","content":"` + promptMarker + `"}]}`
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+testKey)
	r.RemoteAddr = "192.168.8.30:51000"
	w := tg.do(r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), completionMarker) {
		t.Fatalf("got %d %s, want 200 with the completion", w.Code, w.Body.String())
	}
	if !bytes.Contains(tg.backend.body, []byte(promptMarker)) {
		t.Fatal("the prompt did not reach the backend unchanged")
	}
	es := journalEntries(t, tg.journal)
	if len(es) != 1 {
		t.Fatalf("journal holds %d entries, want 1", len(es))
	}
	e := es[0]
	if e.Client != "laptop" || e.Model != "qwen3:14b" || e.Backend != "desktop" || e.Status != 200 ||
		e.In != 120 || e.Out != 40 || e.Source != "192.168.8.30" || e.Time.IsZero() || e.Millis < 0 {
		t.Fatalf("journal entry %+v", e)
	}
	raw := tg.journal.String()
	for _, secret := range []string{promptMarker, completionMarker, testKey, testKey[6:20]} {
		if strings.Contains(raw, secret) {
			t.Fatalf("journal holds %q:\n%s", secret, raw)
		}
	}
}

func TestJournalRecordsARefusalWithoutTheKey(t *testing.T) {
	tg := newTestGateway(t)
	const offered = "llmgw_OFFERED-WRONG-KEY-MARKER-5e2b"
	r := chatRequest("qwen3:14b", offered)
	r.RemoteAddr = "192.168.8.31:51001"
	if w := tg.do(r); w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", w.Code)
	}
	es := journalEntries(t, tg.journal)
	if len(es) != 1 {
		t.Fatalf("journal holds %d entries, want 1", len(es))
	}
	if e := es[0]; e.Status != 401 || e.Reason == "" || e.Source != "192.168.8.31" || e.Client != "" {
		t.Fatalf("journal entry %+v", e)
	}
	raw := tg.journal.String()
	for _, secret := range []string{offered, "OFFERED-WRONG-KEY", promptMarker} {
		if strings.Contains(raw, secret) {
			t.Fatalf("journal holds %q:\n%s", secret, raw)
		}
	}
}

func TestStreamingPassesThroughAndIsRecorded(t *testing.T) {
	tg := newTestGateway(t)
	clientGotFirst := make(chan struct{})
	tg.backend.reply = func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"one\"}}]}\n\n")
		fl.Flush()
		// The second chunk is sent only after the client has read the first, so a gateway that
		// buffers the stream deadlocks here and the test fails on the timeout.
		select {
		case <-clientGotFirst:
		case <-time.After(5 * time.Second):
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\""+completionMarker+"\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":2}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		fl.Flush()
	}
	front := httptest.NewServer(tg.gw)
	defer front.Close()
	body := `{"model":"qwen3:14b","stream":true,"messages":[{"role":"user","content":"` + promptMarker + `"}]}`
	req, _ := http.NewRequest(http.MethodPost, front.URL+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("got %d, want 200", resp.StatusCode)
	}
	sc := bufio.NewScanner(resp.Body)
	first := make(chan string, 1)
	go func() {
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "data: ") {
				first <- sc.Text()
				return
			}
		}
		first <- ""
	}()
	select {
	case line := <-first:
		if !strings.Contains(line, "one") {
			t.Fatalf("first chunk %q", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the first chunk did not reach the client while the backend was still streaming")
	}
	close(clientGotFirst)
	rest, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(rest), completionMarker) || !strings.Contains(string(rest), "[DONE]") {
		t.Fatalf("the rest of the stream: %q", rest)
	}
	resp.Body.Close()
	front.Close() // waits for the handler, so the journal line is written
	es := journalEntries(t, tg.journal)
	if len(es) != 1 {
		t.Fatalf("journal holds %d entries, want 1:\n%s", len(es), tg.journal.String())
	}
	if e := es[0]; e.Status != 200 || !e.Stream || e.In != 7 || e.Out != 2 || e.Client != "laptop" {
		t.Fatalf("journal entry %+v", e)
	}
	if raw := tg.journal.String(); strings.Contains(raw, completionMarker) || strings.Contains(raw, promptMarker) {
		t.Fatalf("journal holds content:\n%s", raw)
	}
}
