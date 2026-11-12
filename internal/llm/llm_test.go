package llm

import (
	"strings"
	"testing"
)

func TestFitDropsOldestTurns(t *testing.T) {
	big := strings.Repeat("x", 400) // ~100 tokens
	var h []Message
	for i := 0; i < 10; i++ {
		h = append(h, Message{"user", big}, Message{"assistant", big})
	}
	h = append(h, Message{"user", "latest"})
	msgs, dropped := Fit("sys", h, 500)
	if msgs[0].Role != "system" || msgs[len(msgs)-1].Content != "latest" {
		t.Fatalf("system first and latest last expected, got %v…%v", msgs[0], msgs[len(msgs)-1])
	}
	if msgs[1].Role != "user" {
		t.Errorf("history should start on a user turn, got %s", msgs[1].Role)
	}
	if dropped == 0 || msgTokens(msgs) > 500 {
		t.Errorf("dropped=%d tokens=%d", dropped, msgTokens(msgs))
	}
}

func TestFitTruncatesHugeMessage(t *testing.T) {
	msgs, _ := Fit("", []Message{{"user", strings.Repeat("y", 100000)}}, 1000)
	if len(msgs) != 1 || msgTokens(msgs) > 1000 {
		t.Errorf("got %d msgs, %d tokens", len(msgs), msgTokens(msgs))
	}
}

func TestBackendRouting(t *testing.T) {
	c := New([]Backend{{Name: "ollama", URL: "http://x/v1/"}})
	b, model, err := c.Backend("ollama/hf.co/org/model:Q4")
	if err != nil || b.URL != "http://x" || model != "hf.co/org/model:Q4" {
		t.Errorf("got %v %q %v", b, model, err)
	}
	if _, _, err := c.Backend("nope/m"); err == nil {
		t.Error("unknown backend should fail")
	}
	if _, _, err := c.Backend("justaname"); err == nil {
		t.Error("model without backend should fail")
	}
}
