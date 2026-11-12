// Package bot connects Telegram chats to local models: each allowed chat has
// its own model, system prompt and history, and replies stream in by
// editing the bot's message as tokens arrive.
package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/iraosandeep/courier/internal/llm"
	"github.com/iraosandeep/courier/internal/telegram"
)

// Options configures a Bot.
type Options struct {
	AllowedUsers []int64
	DefaultModel string // "backend/model"; empty picks the first available
	SystemPrompt string // default for new chats
	StateFile    string // "" or "-" keeps state in memory only
	Logger       *slog.Logger
	// EditInterval is the minimum time between edits of a streaming reply.
	// Telegram allows roughly one message per second per chat.
	EditInterval time.Duration
	PollTimeout  time.Duration
}

type Bot struct {
	tg      *telegram.Client
	llm     *llm.Client
	opts    Options
	log     *slog.Logger
	allowed map[int64]bool
	store   *store

	mu      sync.Mutex
	turns   map[int64]*sync.Mutex        // serializes replies per chat
	cancels map[int64]context.CancelFunc // running reply per chat, for /stop
	warned  map[int64]bool               // strangers already told they're not allowed
}

func New(tg *telegram.Client, models *llm.Client, opts Options) (*Bot, error) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.EditInterval <= 0 {
		opts.EditInterval = 1200 * time.Millisecond
	}
	if opts.PollTimeout <= 0 {
		opts.PollTimeout = 50 * time.Second
	}
	st, err := openStore(opts.StateFile)
	if err != nil {
		return nil, err
	}
	b := &Bot{
		tg: tg, llm: models, opts: opts, log: opts.Logger, store: st,
		allowed: map[int64]bool{},
		turns:   map[int64]*sync.Mutex{},
		cancels: map[int64]context.CancelFunc{},
		warned:  map[int64]bool{},
	}
	for _, id := range opts.AllowedUsers {
		b.allowed[id] = true
	}
	return b, nil
}

var commands = []telegram.BotCommand{
	{Command: "new", Description: "Start a new conversation"},
	{Command: "models", Description: "Pick a model"},
	{Command: "model", Description: "Show or set the model"},
	{Command: "system", Description: "Show or set the system prompt"},
	{Command: "stop", Description: "Stop the current reply"},
	{Command: "help", Description: "How to use this bot"},
}

// Run polls Telegram until ctx is cancelled, then waits for running
// replies to finish.
func (b *Bot) Run(ctx context.Context) error {
	me, err := b.tg.GetMe(ctx)
	if err != nil {
		return fmt.Errorf("checking bot token: %w", err)
	}
	if err := b.tg.SetMyCommands(ctx, commands); err != nil {
		b.log.Warn("could not set command menu", "err", err)
	}
	b.log.Info("bot running", "bot", "@"+me.Username, "allowed_users", b.opts.AllowedUsers)

	var wg sync.WaitGroup
	defer wg.Wait()
	var offset int64
	backoff := time.Second
	for ctx.Err() == nil {
		ups, err := b.tg.GetUpdates(ctx, offset, b.opts.PollTimeout)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			var ae *telegram.APIError
			if errors.As(err, &ae) && ae.Code == 409 {
				b.log.Error("another getUpdates consumer or a webhook is using this bot token; use a separate bot for courier", "err", err)
			} else {
				b.log.Warn("getUpdates failed", "err", err, "retry_in", backoff)
			}
			wait := max(backoff, telegram.RetryAfter(err))
			select {
			case <-ctx.Done():
			case <-time.After(wait):
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		for _, u := range ups {
			offset = u.UpdateID + 1
			wg.Go(func() { b.handle(ctx, u) })
		}
	}
	return nil
}

func (b *Bot) handle(ctx context.Context, u telegram.Update) {
	defer func() {
		if r := recover(); r != nil {
			b.log.Error("panic handling update", "update_id", u.UpdateID, "panic", r)
		}
	}()
	switch {
	case u.CallbackQuery != nil:
		b.handleCallback(ctx, u.CallbackQuery)
	case u.Message != nil:
		b.handleMessage(ctx, u.Message)
	}
}

func (b *Bot) authorized(ctx context.Context, user *telegram.User, chatID int64) bool {
	if user != nil && b.allowed[user.ID] {
		return true
	}
	var uid int64
	if user != nil {
		uid = user.ID
	}
	b.mu.Lock()
	first := !b.warned[uid]
	b.warned[uid] = true
	b.mu.Unlock()
	if first {
		b.log.Warn("ignoring message from user not in allowed_users", "user_id", uid, "chat_id", chatID)
		b.send(ctx, chatID, fmt.Sprintf("Sorry, you're not allowed to use this bot. (Your Telegram user ID is %d.)", uid))
	}
	return false
}

func (b *Bot) handleMessage(ctx context.Context, m *telegram.Message) {
	if !b.authorized(ctx, m.From, m.Chat.ID) {
		return
	}
	text := strings.TrimSpace(m.Text)
	if text == "" {
		text = strings.TrimSpace(m.Caption)
	}
	if text == "" {
		b.send(ctx, m.Chat.ID, "I can only read text messages for now.")
		return
	}
	if strings.HasPrefix(text, "/") {
		cmd, arg, _ := strings.Cut(text, " ")
		cmd, _, _ = strings.Cut(cmd, "@") // /cmd@botname in groups
		b.command(ctx, m.Chat.ID, strings.ToLower(cmd), strings.TrimSpace(arg))
		return
	}
	b.reply(ctx, m.Chat.ID, text)
}

func (b *Bot) send(ctx context.Context, chatID int64, text string) {
	if _, err := b.tg.SendMessage(ctx, chatID, text, nil); err != nil {
		b.log.Warn("sendMessage failed", "chat_id", chatID, "err", err)
	}
}

// model returns the chat's model, falling back to the default and then to
// the first model any backend offers.
func (b *Bot) model(ctx context.Context, chatID int64) (string, error) {
	if m := b.store.get(chatID, b.opts.SystemPrompt).Model; m != "" {
		return m, nil
	}
	if b.opts.DefaultModel != "" {
		return b.opts.DefaultModel, nil
	}
	models, errs := b.llm.Models(ctx)
	if len(models) == 0 {
		return "", fmt.Errorf("no local model server is reachable: %w", errors.Join(errs...))
	}
	return models[0].ID, nil
}

func (b *Bot) turnLock(chatID int64) *sync.Mutex {
	b.mu.Lock()
	defer b.mu.Unlock()
	l, ok := b.turns[chatID]
	if !ok {
		l = &sync.Mutex{}
		b.turns[chatID] = l
	}
	return l
}

// reply runs one conversation turn: it sends the history to the model and
// streams the answer into Telegram.
func (b *Bot) reply(ctx context.Context, chatID int64, text string) {
	lock := b.turnLock(chatID)
	lock.Lock()
	defer lock.Unlock()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	b.mu.Lock()
	b.cancels[chatID] = cancel
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.cancels, chatID)
		b.mu.Unlock()
	}()

	modelID, err := b.model(ctx, chatID)
	if err != nil {
		b.send(ctx, chatID, "⚠️ "+err.Error())
		return
	}
	backend, _, err := b.llm.Backend(modelID)
	if err != nil {
		b.send(ctx, chatID, "⚠️ "+err.Error()+"\nPick another with /models.")
		return
	}
	st := b.store.get(chatID, b.opts.SystemPrompt)
	history := append(st.History, llm.Message{Role: "user", Content: text})
	msgs, dropped := llm.Fit(st.System, history, backend.MaxInputTokens)
	if dropped > 0 {
		b.log.Debug("trimmed history to fit context", "chat_id", chatID, "dropped", dropped)
	}

	out := newLive(b.tg, chatID, b.log)
	typing := b.keepTyping(ctx, chatID)

	var (
		mu    sync.Mutex
		reply strings.Builder
		done  = make(chan struct{})
	)
	go func() { // push partial text to Telegram at most once per EditInterval
		t := time.NewTicker(b.opts.EditInterval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				mu.Lock()
				s := reply.String()
				mu.Unlock()
				if strings.TrimSpace(s) != "" {
					typing()
					out.sync(ctx, s+" ▍", false)
				}
			}
		}
	}()
	err = b.llm.Stream(ctx, modelID, msgs, func(d string) {
		mu.Lock()
		reply.WriteString(d)
		mu.Unlock()
	})
	close(done)
	typing()

	final := strings.TrimSpace(reply.String())
	stopped := errors.Is(ctx.Err(), context.Canceled)
	switch {
	case stopped && final == "":
		final = "⏹ Stopped."
	case stopped:
		final += "\n\n⏹ Stopped."
	case err != nil && final == "":
		final = "⚠️ " + err.Error()
	case err != nil:
		final += "\n\n⚠️ " + err.Error()
	case final == "":
		final = "(The model returned an empty reply.)"
	}
	// Use a fresh context so the final text lands even after /stop.
	fctx, fcancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer fcancel()
	out.sync(fctx, final, true)

	if r := strings.TrimSpace(reply.String()); r != "" {
		b.store.appendTurn(chatID, b.opts.SystemPrompt, text, r)
	}
	if err != nil && !stopped {
		b.log.Warn("model request failed", "chat_id", chatID, "model", modelID, "err", err)
	}
}

// keepTyping shows "typing…" until the returned func is called.
func (b *Bot) keepTyping(ctx context.Context, chatID int64) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		for {
			b.tg.SendChatAction(ctx, chatID, "typing")
			select {
			case <-ctx.Done():
				return
			case <-time.After(4 * time.Second):
			}
		}
	}()
	return cancel
}
