package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iraosandeep/courier/internal/llm"
	"github.com/iraosandeep/courier/internal/telegram"
)

const owner, stranger = 111, 999

// fakeTelegram serves the Bot API methods the bot uses and records the
// text of every message it shows.
type fakeTelegram struct {
	mu      sync.Mutex
	pending []telegram.Update
	nextID  int64
	texts   map[int64]string // message ID → current text
	order   []int64
	answers []string // callback query answers
}

func (f *fakeTelegram) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	defer f.mu.Unlock()
	var result any = true
	switch method {
	case "getMe":
		result = telegram.User{ID: 1, IsBot: true, Username: "testbot"}
	case "getUpdates":
		result, f.pending = f.pending, nil
		if result.([]telegram.Update) == nil {
			result = []telegram.Update{}
		}
	case "sendMessage":
		f.nextID++
		f.texts[f.nextID] = body["text"].(string)
		f.order = append(f.order, f.nextID)
		result = telegram.Message{MessageID: f.nextID}
	case "editMessageText":
		id := int64(body["message_id"].(float64))
		if f.texts[id] == body["text"].(string) {
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": 400, "description": "Bad Request: message is not modified"})
			return
		}
		f.texts[id] = body["text"].(string)
	case "answerCallbackQuery":
		f.answers = append(f.answers, body["text"].(string))
	}
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

func (f *fakeTelegram) push(u telegram.Update) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u.UpdateID = int64(len(f.order)*100 + len(f.pending) + 1)
	f.pending = append(f.pending, u)
}

func (f *fakeTelegram) messages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, id := range f.order {
		out = append(out, f.texts[id])
	}
	return out
}

// fakeModel is an OpenAI-compatible server that streams a canned reply and
// records the prompts it receives.
type fakeModel struct {
	mu      sync.Mutex
	reply   string
	prompts [][]llm.Message
	models  []string
}

func (f *fakeModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/models" {
		var data []map[string]string
		for _, m := range f.models {
			data = append(data, map[string]string{"id": m})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
		return
	}
	var req struct {
		Messages []llm.Message `json:"messages"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	f.mu.Lock()
	f.prompts = append(f.prompts, req.Messages)
	reply := f.reply
	f.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	for _, word := range strings.SplitAfter(reply, " ") {
		b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": word}}}})
		fmt.Fprintf(w, "data: %s\n\n", b)
		w.(http.Flusher).Flush()
	}
	io.WriteString(w, "data: [DONE]\n\n")
}

func (f *fakeModel) lastPrompt() []llm.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.prompts) == 0 {
		return nil
	}
	return f.prompts[len(f.prompts)-1]
}

type harness struct {
	tg    *fakeTelegram
	model *fakeModel
	state string
	stop  func()
}

func start(t *testing.T, state string) *harness {
	t.Helper()
	h := &harness{
		tg:    &fakeTelegram{texts: map[int64]string{}},
		model: &fakeModel{reply: "Hello from the local model.", models: []string{"alpha", "beta"}},
		state: state,
	}
	tgSrv := httptest.NewServer(h.tg)
	llmSrv := httptest.NewServer(h.model)
	b, err := New(telegram.New("TOKEN", tgSrv.URL), llm.New([]llm.Backend{{Name: "local", URL: llmSrv.URL}}), Options{
		AllowedUsers: []int64{owner},
		SystemPrompt: "Be brief.",
		StateFile:    state,
		Logger:       slog.New(slog.DiscardHandler),
		EditInterval: 10 * time.Millisecond,
		PollTimeout:  50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.Run(ctx); close(done) }()
	h.stop = func() { cancel(); <-done; tgSrv.Close(); llmSrv.Close() }
	t.Cleanup(h.stop)
	return h
}

func (h *harness) say(from int64, text string) {
	h.tg.push(telegram.Update{Message: &telegram.Message{
		From: &telegram.User{ID: from, FirstName: "x"}, Chat: telegram.Chat{ID: from, Type: "private"}, Text: text,
	}})
}

// waitFor polls until the last message shown satisfies ok.
func (h *harness) waitFor(t *testing.T, ok func(last string) bool) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ms := h.tg.messages(); len(ms) > 0 && ok(ms[len(ms)-1]) {
			return ms[len(ms)-1]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out; messages: %q", h.tg.messages())
	return ""
}

func TestStrangerIsRefused(t *testing.T) {
	h := start(t, "-")
	h.say(stranger, "hi")
	got := h.waitFor(t, func(s string) bool { return strings.Contains(s, "not allowed") })
	if !strings.Contains(got, "999") {
		t.Errorf("refusal should tell the user their ID, got %q", got)
	}
	if h.model.lastPrompt() != nil {
		t.Error("model was called for an unauthorized user")
	}
}

func TestReplyStreamsAndKeepsHistory(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state.json")
	h := start(t, state)

	h.say(owner, "first question")
	h.waitFor(t, func(s string) bool { return s == "Hello from the local model." })

	h.model.mu.Lock()
	h.model.reply = "Second answer."
	h.model.mu.Unlock()
	h.say(owner, "second question")
	h.waitFor(t, func(s string) bool { return s == "Second answer." })

	p := h.model.lastPrompt()
	want := []llm.Message{
		{Role: "system", Content: "Be brief."},
		{Role: "user", Content: "first question"},
		{Role: "assistant", Content: "Hello from the local model."},
		{Role: "user", Content: "second question"},
	}
	if fmt.Sprint(p) != fmt.Sprint(want) {
		t.Errorf("prompt =\n%v\nwant\n%v", p, want)
	}

	// History survives a restart via the state file.
	h.stop()
	h2 := start(t, state)
	h2.say(owner, "third")
	h2.waitFor(t, func(s string) bool { return s == "Hello from the local model." })
	if n := len(h2.model.lastPrompt()); n != 6 {
		t.Errorf("after restart prompt has %d messages, want 6", n)
	}
}

func TestNewClearsHistory(t *testing.T) {
	h := start(t, "-")
	h.say(owner, "one")
	h.waitFor(t, func(s string) bool { return s == "Hello from the local model." })
	h.say(owner, "/new")
	h.waitFor(t, func(s string) bool { return strings.Contains(s, "New conversation") })
	h.say(owner, "two")
	h.waitFor(t, func(s string) bool { return s == "Hello from the local model." })
	if n := len(h.model.lastPrompt()); n != 2 {
		t.Errorf("prompt after /new has %d messages, want 2 (system + user)", n)
	}
}

func TestSwitchModelWithButton(t *testing.T) {
	h := start(t, "-")
	h.say(owner, "/models")
	h.waitFor(t, func(s string) bool { return strings.HasPrefix(s, "Pick a model") })
	msgID := int64(len(h.tg.messages()))
	h.tg.push(telegram.Update{CallbackQuery: &telegram.CallbackQuery{
		ID: "q1", From: telegram.User{ID: owner}, Data: "m:local/beta",
		Message: &telegram.Message{MessageID: msgID, Chat: telegram.Chat{ID: owner}},
	}})
	h.waitFor(t, func(s string) bool { return s == "✅ Model: local/beta" })
	h.say(owner, "/model")
	h.waitFor(t, func(s string) bool { return strings.HasPrefix(s, "Model: local/beta") })

	h.say(owner, "/model local/nope")
	h.waitFor(t, func(s string) bool { return strings.Contains(s, "isn't available") })
}

func TestSplit(t *testing.T) {
	long := strings.Repeat("word ", 2000) // 10000 chars
	parts := split(long, 4000)
	if len(parts) != 3 {
		t.Fatalf("got %d parts", len(parts))
	}
	for _, p := range parts {
		if len(p) > 4000 || strings.HasSuffix(p, " ") || strings.HasPrefix(p, " ") {
			t.Errorf("bad part of len %d", len(p))
		}
	}
	if strings.Join(parts, " ") != strings.TrimSpace(long) {
		t.Error("split lost text")
	}
	// Emoji count as two UTF-16 units.
	if got := split(strings.Repeat("😀", 3000), 4000); len(got) != 2 {
		t.Errorf("emoji split into %d parts, want 2", len(got))
	}
	// Earlier parts don't change as the text grows.
	a, b := split(long[:6000], 4000), split(long, 4000)
	if a[0] != b[0] {
		t.Error("first part changed as text grew")
	}
	if got := split("short", 4000); len(got) != 1 || got[0] != "short" {
		t.Errorf("split(short) = %q", got)
	}
}
