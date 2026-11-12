// Courier: chat with the AI models running on your computer from Telegram.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/iraosandeep/courier/internal/bot"
	"github.com/iraosandeep/courier/internal/config"
	"github.com/iraosandeep/courier/internal/llm"
	"github.com/iraosandeep/courier/internal/telegram"
)

// version is set at build time with -ldflags "-X main.version=…".
var version = "dev"

const usage = `Courier: chat with the AI models running on your computer from Telegram.

Usage:
  courier setup      connect a Telegram bot and choose who may use it
  courier [run]      start the bot
  courier models     list the models Courier can reach
  courier version

Flags:
  -config PATH       config file (default ~/.config/courier/config.json)
  -debug             verbose logging (run only)

Environment:
  COURIER_BOT_TOKEN      Telegram bot token (from @BotFather)
  COURIER_ALLOWED_USERS  comma-separated Telegram user IDs
  COURIER_DEFAULT_MODEL  e.g. fm/system or ollama/llama3.2
  COURIER_CONFIG         config file path
`

func main() {
	args := os.Args[1:]
	cmd := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	cfgPath := fs.String("config", "", "config file")
	debug := fs.Bool("debug", false, "verbose logging")
	fs.Parse(args)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch cmd {
	case "run", "serve":
		err = run(ctx, *cfgPath, *debug)
	case "setup", "init":
		err = setup(ctx, *cfgPath)
	case "models":
		err = listModels(ctx, *cfgPath)
	case "version":
		fmt.Println("courier", version)
	case "help":
		fmt.Print(usage)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "courier:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, path string, debug bool) error {
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	models := llm.New(cfg.Backends)
	found, errs := models.Models(ctx)
	for _, m := range found {
		logger.Info("model available", "model", m.ID)
	}
	for _, e := range errs {
		logger.Debug("backend not reachable", "err", e)
	}
	if len(found) == 0 {
		logger.Warn("no local model server is reachable yet; start one (e.g. fm serve, ollama serve) and Courier will use it")
	}

	b, err := bot.New(telegram.New(cfg.BotToken, cfg.TelegramAPIURL), models, bot.Options{
		AllowedUsers: cfg.AllowedUsers,
		DefaultModel: cfg.DefaultModel,
		SystemPrompt: cfg.SystemPrompt,
		StateFile:    cfg.StateFile,
		Logger:       logger,
	})
	if err != nil {
		return err
	}
	return b.Run(ctx)
}

func listModels(ctx context.Context, path string) error {
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	models, errs := llm.New(cfg.Backends).Models(ctx)
	for _, m := range models {
		fmt.Println(m.ID)
	}
	for _, e := range errs {
		fmt.Fprintln(os.Stderr, "  (skipped)", e)
	}
	if len(models) == 0 {
		return errors.New("no models found; is a local model server running?")
	}
	return nil
}

// setup walks through creating the config: bot token, then the owner's
// user ID (captured from a message they send the bot), then a model.
func setup(ctx context.Context, path string) error {
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	in := bufio.NewReader(os.Stdin)

	fmt.Println("1. In Telegram, message @BotFather, send /newbot and copy the token.")
	fmt.Println("   Use a bot just for Courier: a bot can only have one program reading its messages.")
	token := cfg.BotToken
	if token != "" {
		fmt.Print("   Bot token [keep current]: ")
	} else {
		fmt.Print("   Bot token: ")
	}
	t, err := readSecret(in)
	if err != nil {
		return err
	}
	if t != "" {
		token = t
	}
	if token == "" {
		return errors.New("a bot token is required")
	}
	tg := telegram.New(token, cfg.TelegramAPIURL)
	me, err := tg.GetMe(ctx)
	if err != nil {
		return fmt.Errorf("that token didn't work: %w", err)
	}
	cfg.BotToken = token
	fmt.Printf("   ✓ Connected to @%s\n\n", me.Username)

	fmt.Printf("2. Send any message to @%s from your Telegram account (https://t.me/%s).\n", me.Username, me.Username)
	fmt.Println("   Waiting… (Ctrl+C to cancel)")
	user, err := waitForUser(ctx, tg)
	if err != nil {
		return err
	}
	name := user.FirstName
	if user.Username != "" {
		name += " (@" + user.Username + ")"
	}
	fmt.Printf("   ✓ Got a message from %s, user ID %d\n", name, user.ID)
	if !slices.Contains(cfg.AllowedUsers, user.ID) {
		cfg.AllowedUsers = append(cfg.AllowedUsers, user.ID)
	}
	fmt.Printf("   Only these users can use the bot: %v (edit allowed_users in the config to change)\n\n", cfg.AllowedUsers)

	fmt.Println("3. Looking for local models…")
	models, _ := llm.New(cfg.Backends).Models(ctx)
	if len(models) == 0 {
		fmt.Println("   None running right now. Start one (fm serve, ollama serve, LM Studio…) before chatting.")
	} else {
		for i, m := range models {
			fmt.Printf("   %d) %s\n", i+1, m.ID)
		}
		fmt.Print("   Default model [1]: ")
		line, _ := in.ReadString('\n')
		n := 1
		fmt.Sscanf(strings.TrimSpace(line), "%d", &n)
		if n >= 1 && n <= len(models) {
			cfg.DefaultModel = models[n-1].ID
		}
	}

	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("\n✓ Saved %s\n", cfg.Path)
	if _, err := tg.SendMessage(ctx, user.ID, "✅ Courier is set up. Start courier on your computer, then message me here.", nil); err != nil {
		fmt.Println("  (couldn't send a confirmation message:", err, ")")
	}
	fmt.Println("Start the bot with: courier")
	return nil
}

func waitForUser(ctx context.Context, tg *telegram.Client) (*telegram.User, error) {
	// Skip anything sent before setup started.
	var offset int64
	if ups, err := tg.GetUpdates(ctx, -1, 0); err == nil && len(ups) > 0 {
		offset = ups[len(ups)-1].UpdateID + 1
	}
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		ups, err := tg.GetUpdates(ctx, offset, 25*time.Second)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			var ae *telegram.APIError
			if errors.As(err, &ae) && ae.Code == 409 {
				return nil, errors.New("this bot is already being read by another program (a running courier, or a webhook); stop it and retry")
			}
			time.Sleep(2 * time.Second)
			continue
		}
		for _, u := range ups {
			offset = u.UpdateID + 1
			if u.Message != nil && u.Message.From != nil && !u.Message.From.IsBot {
				tg.GetUpdates(ctx, offset, 0) // acknowledge it so `courier run` doesn't answer it
				return u.Message.From, nil
			}
		}
	}
	return nil, errors.New("no message arrived within 5 minutes")
}

func readSecret(in *bufio.Reader) (string, error) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		return strings.TrimSpace(string(b)), err
	}
	line, err := in.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
