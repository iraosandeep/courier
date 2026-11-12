// Package telegram is a minimal Telegram Bot API client for a long-polling
// bot, built on net/http. Errors never contain the bot token.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultBaseURL is the public Telegram Bot API endpoint.
const DefaultBaseURL = "https://api.telegram.org"

// MaxMessageLen is Telegram's limit on message text, in UTF-16 code units.
const MaxMessageLen = 4096

const maxResponseBytes = 4 << 20

// Client calls the Telegram Bot API. It is safe for concurrent use.
type Client struct {
	token   string
	baseURL string
	http    *http.Client
}

// New returns a Client for the given bot token. An empty baseURL selects
// DefaultBaseURL.
func New(token, baseURL string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		token:   token,
		baseURL: strings.TrimRight(baseURL, "/"),
		// No overall timeout: long polls are bounded by the request context.
		http: &http.Client{},
	}
}

// APIError is an error reported by Telegram (ok: false).
type APIError struct {
	Method      string
	Code        int
	Description string
	RetryAfter  time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram %s: %d %s", e.Method, e.Code, e.Description)
}

// IsNotModified reports whether err is Telegram refusing an edit that would
// leave the message unchanged.
func IsNotModified(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && strings.Contains(ae.Description, "message is not modified")
}

// RetryAfter returns how long Telegram asked us to wait, or 0.
func RetryAfter(err error) time.Duration {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.RetryAfter
	}
	return 0
}

type envelope struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  *struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

func (c *Client) call(ctx context.Context, method string, payload, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/bot"+c.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return c.redact(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return c.redact(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return c.redact(err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("telegram %s: %s: unreadable response", method, resp.Status)
	}
	if !env.OK {
		ae := &APIError{Method: method, Code: env.ErrorCode, Description: env.Description}
		if env.Parameters != nil {
			ae.RetryAfter = time.Duration(env.Parameters.RetryAfter) * time.Second
		}
		return ae
	}
	if out != nil {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

// redact strips the bot token from errors (net/http puts the URL in them).
func (c *Client) redact(err error) error {
	if c.token == "" || !strings.Contains(err.Error(), c.token) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), c.token, "<token>"))
}

// GetMe returns the bot's own user, which also validates the token.
func (c *Client) GetMe(ctx context.Context) (*User, error) {
	var u User
	return &u, c.call(ctx, "getMe", struct{}{}, &u)
}

// GetUpdates long-polls for updates with ID >= offset, waiting up to
// timeout for one to arrive.
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeout time.Duration) ([]Update, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout+15*time.Second)
	defer cancel()
	var ups []Update
	err := c.call(ctx, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         int(timeout / time.Second),
		"allowed_updates": []string{"message", "callback_query"},
	}, &ups)
	return ups, err
}

// SendMessage sends plain text (no parse_mode, so nothing needs escaping).
func (c *Client) SendMessage(ctx context.Context, chatID int64, text string, markup *InlineKeyboardMarkup) (*Message, error) {
	var m Message
	err := c.call(ctx, "sendMessage", map[string]any{
		"chat_id":              chatID,
		"text":                 text,
		"reply_markup":         markup,
		"link_preview_options": map[string]bool{"is_disabled": true},
	}, &m)
	return &m, err
}

// EditMessageText replaces a message's text.
func (c *Client) EditMessageText(ctx context.Context, chatID, messageID int64, text string, markup *InlineKeyboardMarkup) error {
	return c.call(ctx, "editMessageText", map[string]any{
		"chat_id":              chatID,
		"message_id":           messageID,
		"text":                 text,
		"reply_markup":         markup,
		"link_preview_options": map[string]bool{"is_disabled": true},
	}, nil)
}

// SendChatAction shows e.g. "typing…" in the chat for about five seconds.
func (c *Client) SendChatAction(ctx context.Context, chatID int64, action string) error {
	return c.call(ctx, "sendChatAction", map[string]any{"chat_id": chatID, "action": action}, nil)
}

// AnswerCallbackQuery acknowledges an inline-button press.
func (c *Client) AnswerCallbackQuery(ctx context.Context, id, text string) error {
	return c.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": id, "text": text}, nil)
}

// SetMyCommands sets the command menu shown in Telegram clients.
func (c *Client) SetMyCommands(ctx context.Context, cmds []BotCommand) error {
	return c.call(ctx, "setMyCommands", map[string]any{"commands": cmds}, nil)
}
