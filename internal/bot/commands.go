package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/iraosandeep/courier/internal/telegram"
)

const helpText = `Chat with the AI models running on your computer. Just send a message.

/new – start a new conversation
/models – pick a model
/model <backend/model> – switch model by name
/system <text> – set the system prompt (/system reset restores the default)
/stop – stop the current reply

Current model: %s`

func (b *Bot) command(ctx context.Context, chatID int64, cmd, arg string) {
	switch cmd {
	case "/start", "/help":
		model, err := b.model(ctx, chatID)
		if err != nil {
			model = "none (" + err.Error() + ")"
		}
		b.send(ctx, chatID, fmt.Sprintf(helpText, model))

	case "/new", "/reset", "/clear":
		b.store.update(chatID, b.opts.SystemPrompt, func(c *chatState) { c.History = nil })
		b.send(ctx, chatID, "🧹 New conversation.")

	case "/stop":
		b.mu.Lock()
		cancel := b.cancels[chatID]
		b.mu.Unlock()
		if cancel == nil {
			b.send(ctx, chatID, "Nothing to stop.")
			return
		}
		cancel()

	case "/models":
		b.showModels(ctx, chatID)

	case "/model":
		if arg == "" {
			model, err := b.model(ctx, chatID)
			if err != nil {
				b.send(ctx, chatID, "⚠️ "+err.Error())
				return
			}
			b.send(ctx, chatID, "Model: "+model+"\nChange it with /models.")
			return
		}
		if err := b.setModel(ctx, chatID, arg); err != nil {
			b.send(ctx, chatID, "⚠️ "+err.Error())
			return
		}
		b.send(ctx, chatID, "✅ Model: "+arg)

	case "/system":
		switch {
		case arg == "":
			sys := b.store.get(chatID, b.opts.SystemPrompt).System
			if sys == "" {
				sys = "(none)"
			}
			b.send(ctx, chatID, "System prompt:\n"+sys+"\n\nChange it with /system <text>, or /system reset.")
		case strings.EqualFold(arg, "reset"):
			b.store.update(chatID, b.opts.SystemPrompt, func(c *chatState) { c.System = b.opts.SystemPrompt })
			b.send(ctx, chatID, "✅ System prompt reset.")
		default:
			b.store.update(chatID, b.opts.SystemPrompt, func(c *chatState) { c.System = arg })
			b.send(ctx, chatID, "✅ System prompt set. It applies from your next message.")
		}

	default:
		b.send(ctx, chatID, "Unknown command. Try /help.")
	}
}

// setModel switches the chat's model after checking a backend offers it.
func (b *Bot) setModel(ctx context.Context, chatID int64, id string) error {
	if _, _, err := b.llm.Backend(id); err != nil {
		return err
	}
	models, errs := b.llm.Models(ctx)
	found := false
	for _, m := range models {
		found = found || m.ID == id
	}
	if !found {
		if len(errs) > 0 {
			return fmt.Errorf("%s isn't available. %w", id, errors.Join(errs...))
		}
		return fmt.Errorf("%s isn't available. See /models", id)
	}
	b.store.update(chatID, b.opts.SystemPrompt, func(c *chatState) { c.Model = id })
	return nil
}

const modelCallback = "m:"

func (b *Bot) showModels(ctx context.Context, chatID int64) {
	models, errs := b.llm.Models(ctx)
	if len(models) == 0 {
		b.send(ctx, chatID, "⚠️ No local model server is reachable.\n"+errors.Join(errs...).Error())
		return
	}
	current, _ := b.model(ctx, chatID)
	var kb telegram.InlineKeyboardMarkup
	var long []string
	for _, m := range models {
		label := m.ID
		if m.ID == current {
			label = "✓ " + label
		}
		data := modelCallback + m.ID
		if len(data) > 64 { // Telegram's callback_data limit
			long = append(long, m.ID)
			continue
		}
		kb.InlineKeyboard = append(kb.InlineKeyboard, []telegram.InlineKeyboardButton{{Text: label, CallbackData: data}})
	}
	text := "Pick a model:"
	if len(long) > 0 {
		text += "\n\nName too long for a button; use /model <name>:\n" + strings.Join(long, "\n")
	}
	if len(errs) > 0 {
		text += "\n\nUnreachable: " + strings.Join(backendNames(errs), ", ")
	}
	if _, err := b.tg.SendMessage(ctx, chatID, text, &kb); err != nil {
		b.log.Warn("sendMessage failed", "chat_id", chatID, "err", err)
	}
}

// backendNames shortens "ollama unreachable at …" errors to "ollama".
func backendNames(errs []error) []string {
	var names []string
	for _, e := range errs {
		name, _, _ := strings.Cut(e.Error(), " ")
		names = append(names, strings.TrimSuffix(name, ":"))
	}
	return names
}

func (b *Bot) handleCallback(ctx context.Context, q *telegram.CallbackQuery) {
	if q.Message == nil {
		return
	}
	chatID := q.Message.Chat.ID
	if !b.authorized(ctx, &q.From, chatID) {
		b.tg.AnswerCallbackQuery(ctx, q.ID, "Not allowed")
		return
	}
	id, ok := strings.CutPrefix(q.Data, modelCallback)
	if !ok {
		b.tg.AnswerCallbackQuery(ctx, q.ID, "")
		return
	}
	if err := b.setModel(ctx, chatID, id); err != nil {
		b.tg.AnswerCallbackQuery(ctx, q.ID, err.Error())
		return
	}
	b.tg.AnswerCallbackQuery(ctx, q.ID, "Switched to "+id)
	b.tg.EditMessageText(ctx, chatID, q.Message.MessageID, "✅ Model: "+id, nil)
}
