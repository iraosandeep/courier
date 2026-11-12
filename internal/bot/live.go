package bot

import (
	"context"
	"log/slog"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/iraosandeep/courier/internal/telegram"
)

// chunkLimit is a little under telegram.MaxMessageLen, leaving room for the
// streaming cursor and the "Stopped" suffix.
const chunkLimit = telegram.MaxMessageLen - 64

// live is a reply that grows while the model streams. Text longer than one
// Telegram message spills over into further messages.
type live struct {
	tg     *telegram.Client
	chatID int64
	log    *slog.Logger
	ids    []int64  // messages sent so far
	shown  []string // text currently displayed in each
}

func newLive(tg *telegram.Client, chatID int64, log *slog.Logger) *live {
	return &live{tg: tg, chatID: chatID, log: log}
}

// sync makes the chat show full. While streaming, failed edits (usually
// rate limits) are skipped since a newer edit follows soon; the final sync
// retries until it lands.
func (l *live) sync(ctx context.Context, full string, final bool) {
	parts := split(full, chunkLimit)
	for i, p := range parts {
		if i < len(l.shown) && l.shown[i] == p {
			continue
		}
		err := l.try(ctx, final, func() error {
			if i < len(l.ids) {
				err := l.tg.EditMessageText(ctx, l.chatID, l.ids[i], p, nil)
				if telegram.IsNotModified(err) {
					return nil
				}
				return err
			}
			m, err := l.tg.SendMessage(ctx, l.chatID, p, nil)
			if err == nil {
				l.ids = append(l.ids, m.MessageID)
				l.shown = append(l.shown, "")
			}
			return err
		})
		if err != nil {
			if final {
				l.log.Error("sending reply failed", "chat_id", l.chatID, "err", err)
			} else {
				l.log.Debug("updating partial reply failed", "chat_id", l.chatID, "err", err)
			}
			return
		}
		l.shown[i] = p
	}
	// The final text can need fewer messages than the streamed text did
	// (the cursor pushed it over a boundary); blank out the leftovers.
	if final {
		for i := len(parts); i < len(l.ids); i++ {
			l.tg.EditMessageText(ctx, l.chatID, l.ids[i], "·", nil)
		}
	}
}

func (l *live) try(ctx context.Context, final bool, op func() error) error {
	for attempt := 0; ; attempt++ {
		err := op()
		if err == nil || !final || attempt >= 4 {
			return err
		}
		wait := telegram.RetryAfter(err)
		if wait == 0 {
			wait = time.Duration(attempt+1) * time.Second
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(wait):
		}
	}
}

// split cuts s into pieces of at most limit UTF-16 code units (how Telegram
// counts), preferring to break at a newline, then at a space. Pieces depend
// only on the text before them, so earlier pieces stay stable as s grows.
func split(s string, limit int) []string {
	var parts []string
	for {
		r := []rune(s)
		n, cut := 0, len(r)
		for i, c := range r {
			n += utf16.RuneLen(c)
			if n > limit {
				cut = i
				break
			}
		}
		if cut == len(r) {
			if s = strings.TrimRight(s, " \n"); s != "" || len(parts) == 0 {
				parts = append(parts, s)
			}
			return parts
		}
		chunk := string(r[:cut])
		if i := strings.LastIndex(chunk, "\n"); i > len(chunk)/2 {
			chunk = chunk[:i+1]
		} else if i := strings.LastIndex(chunk, " "); i > len(chunk)/2 {
			chunk = chunk[:i+1]
		}
		if p := strings.TrimRight(chunk, " \n"); p != "" {
			parts = append(parts, p)
		}
		s = strings.TrimLeft(s[len(chunk):], " \n")
		if s == "" {
			return parts
		}
	}
}
