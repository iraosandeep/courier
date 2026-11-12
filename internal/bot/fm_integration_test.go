package bot

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iraosandeep/courier/internal/llm"
	"github.com/iraosandeep/courier/internal/telegram"
)

// TestRealFM talks to a running `fm serve`. Run with COURIER_FM_TEST=1.
func TestRealFM(t *testing.T) {
	if os.Getenv("COURIER_FM_TEST") == "" {
		t.Skip("set COURIER_FM_TEST=1 with fm serve running on :1976")
	}
	tg := &fakeTelegram{texts: map[int64]string{}}
	srv := httptest.NewServer(tg)
	defer srv.Close()
	b, err := New(telegram.New("TOKEN", srv.URL), llm.New([]llm.Backend{{Name: "fm", URL: "http://127.0.0.1:1976"}}), Options{
		AllowedUsers: []int64{owner}, StateFile: "-", Logger: slog.New(slog.DiscardHandler),
		EditInterval: 300 * time.Millisecond, PollTimeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)
	h := &harness{tg: tg}
	h.say(owner, "Write three short sentences about apples.")
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		ms := tg.messages()
		if len(ms) > 0 && !strings.HasSuffix(ms[len(ms)-1], "▍") && len(ms[len(ms)-1]) > 20 {
			t.Logf("reply: %s", ms[len(ms)-1])
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("no final reply: %q", tg.messages())
}
