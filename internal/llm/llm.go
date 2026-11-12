// Package llm talks to local OpenAI-compatible chat servers: Apple's
// `fm serve` (Orchard), Ollama, LM Studio, llama.cpp's llama-server, vLLM…
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Message is one chat turn.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Backend is one OpenAI-compatible server.
type Backend struct {
	Name   string `json:"name"`
	URL    string `json:"url"` // base URL, without /v1
	APIKey string `json:"api_key,omitempty"`
	// MaxInputTokens bounds the prompt (system + history). Older turns are
	// dropped to fit. Default 3000, which suits fm serve's 4096 context.
	MaxInputTokens int `json:"max_input_tokens,omitempty"`
}

// Model is a model offered by a backend. ID is "backend/model".
type Model struct {
	ID      string
	Backend string
	Name    string
}

// Client routes requests to backends by model ID.
type Client struct {
	backends []Backend
	http     *http.Client
}

func New(backends []Backend) *Client {
	bs := make([]Backend, len(backends))
	for i, b := range backends {
		b.URL = strings.TrimSuffix(strings.TrimRight(b.URL, "/"), "/v1")
		if b.MaxInputTokens <= 0 {
			b.MaxInputTokens = 3000
		}
		bs[i] = b
	}
	return &Client{backends: bs, http: &http.Client{}}
}

func (c *Client) Backends() []Backend { return c.backends }

// Backend returns the backend that serves modelID.
func (c *Client) Backend(modelID string) (Backend, string, error) {
	name, model, ok := strings.Cut(modelID, "/")
	if !ok || model == "" {
		return Backend{}, "", fmt.Errorf("model %q should look like backend/model", modelID)
	}
	for _, b := range c.backends {
		if b.Name == name {
			return b, model, nil
		}
	}
	return Backend{}, "", fmt.Errorf("no backend named %q", name)
}

func (c *Client) request(ctx context.Context, b Backend, method, path string, body any) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, b.URL+path, r)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if b.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+b.APIKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s unreachable at %s (is it running?)", b.Name, b.URL)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("%s: %s: %s", b.Name, resp.Status, strings.TrimSpace(string(msg)))
	}
	return resp, nil
}

// Models lists the models of every reachable backend, querying them in
// parallel. Errors from unreachable backends are returned alongside.
func (c *Client) Models(ctx context.Context) ([]Model, []error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var (
		mu     sync.Mutex
		models []Model
		errs   []error
		wg     sync.WaitGroup
	)
	for _, b := range c.backends {
		wg.Go(func() {
			ms, err := c.listModels(ctx, b)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			models = append(models, ms...)
		})
	}
	wg.Wait()
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, errs
}

func (c *Client) listModels(ctx context.Context, b Backend) ([]Model, error) {
	resp, err := c.request(ctx, b, "GET", "/v1/models", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var r struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("%s: bad /v1/models response: %w", b.Name, err)
	}
	var ms []Model
	for _, d := range r.Data {
		ms = append(ms, Model{ID: b.Name + "/" + d.ID, Backend: b.Name, Name: d.ID})
	}
	return ms, nil
}

// Stream sends msgs to modelID and calls onDelta with each chunk of reply.
func (c *Client) Stream(ctx context.Context, modelID string, msgs []Message, onDelta func(string)) error {
	b, model, err := c.Backend(modelID)
	if err != nil {
		return err
	}
	resp, err := c.request(ctx, b, "POST", "/v1/chat/completions", map[string]any{
		"model":    model,
		"messages": msgs,
		"stream":   true,
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Some servers ignore stream:true and answer with plain JSON.
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		var r struct {
			Choices []struct {
				Message Message `json:"message"`
			} `json:"choices"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			return err
		}
		if len(r.Choices) > 0 {
			onDelta(r.Choices[0].Message.Content)
		}
		return nil
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			return nil
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}
		if chunk.Error != nil {
			return errors.New(b.Name + ": " + chunk.Error.Message)
		}
		for _, ch := range chunk.Choices {
			if ch.Delta.Content != "" {
				onDelta(ch.Delta.Content)
			}
		}
	}
	return sc.Err()
}

// EstTokens estimates tokens as characters ÷ 4.
func EstTokens(s string) int { return (utf8.RuneCountInString(s) + 3) / 4 }

func msgTokens(msgs []Message) int {
	n := 0
	for _, m := range msgs {
		n += EstTokens(m.Content) + 4 // small per-message overhead
	}
	return n
}

// Fit drops the oldest turns until system + history fits budget tokens. The
// last message is always kept, truncated if it alone is too big. It returns
// the messages to send (system first) and how many turns were dropped.
func Fit(system string, history []Message, budget int) ([]Message, int) {
	var head []Message
	if strings.TrimSpace(system) != "" {
		head = []Message{{Role: "system", Content: system}}
	}
	dropped := 0
	for len(history) > 1 && msgTokens(head)+msgTokens(history) > budget {
		history = history[1:]
		dropped++
	}
	// Don't start on an assistant turn after dropping.
	for len(history) > 1 && history[0].Role == "assistant" {
		history = history[1:]
		dropped++
	}
	if len(history) == 1 {
		room := max(budget-msgTokens(head)-4, 64) * 4
		last := history[0]
		if r := []rune(last.Content); len(r) > room {
			last.Content = string(r[:room])
		}
		history = []Message{last}
	}
	return append(head, history...), dropped
}
