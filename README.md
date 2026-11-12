# Courier

**Chat with the AI models running on your computer, from Telegram.**

Courier is a small Telegram bot that runs on your computer and forwards your messages to local models. It works with Apple's on-device model via `fm serve` (what [Orchard](https://github.com/iraosandeep/Orchard) uses), and with Ollama, LM Studio, llama.cpp, or any other OpenAI-compatible server. Replies stream in as the model writes them.

- **No server, no open ports:** Courier long-polls Telegram, so it works behind NAT and on a laptop.
- **Private by default:** only the Telegram user IDs you allow can use it. Everyone else is refused.
- **One static binary:** no database or Docker. Chats are saved to a JSON file only you can read.

Its sibling [Dispatch](https://github.com/iraosandeep/dispatch) sends notifications *to* Telegram. Courier lets you talk *back*.

## Quick start

```sh
go install github.com/iraosandeep/courier/cmd/courier@latest
courier setup   # paste a bot token from @BotFather, then message your bot once
courier         # start the bot
```

Then message your bot in Telegram.

Give Courier **its own bot**. Telegram lets only one program read a bot's messages, so don't reuse Dispatch's bot.

## Commands

| | |
|---|---|
| *any text* | chat with the current model |
| `/new` | start a new conversation |
| `/models` | pick a model from buttons |
| `/model ollama/llama3.2` | switch model by name |
| `/system <text>` | set the system prompt for this chat (`/system reset` restores the default) |
| `/stop` | stop the reply being written |

Each chat keeps its own model, system prompt and history. Older turns are dropped automatically to fit the model's context window.

## Models

By default Courier looks for these servers on their usual ports and uses whichever are running:

| Backend | URL | Start it with |
|---|---|---|
| `fm` | `http://127.0.0.1:1976` | `fm serve`, or just run Orchard |
| `ollama` | `http://127.0.0.1:11434` | `ollama serve` |
| `lmstudio` | `http://127.0.0.1:1234` | LM Studio › Developer › Start server |
| `llamacpp` | `http://127.0.0.1:8080` | `llama-server -m model.gguf` |

Model names are `backend/model`, for example `fm/system` or `ollama/qwen3:8b`. `courier models` lists the models it can reach.

## Configuration

`courier setup` writes `~/.config/courier/config.json` with owner-only permissions:

```json
{
  "bot_token": "123456:ABC…",
  "allowed_users": [123456789],
  "default_model": "fm/system",
  "system_prompt": "You are a helpful assistant. Keep answers short; they're read on a phone.",
  "backends": [
    { "name": "fm", "url": "http://127.0.0.1:1976", "max_input_tokens": 3000 },
    { "name": "ollama", "url": "http://127.0.0.1:11434", "max_input_tokens": 12000 },
    { "name": "remote", "url": "http://my-gpu-box:8000", "api_key": "…" }
  ]
}
```

- `max_input_tokens` caps the prompt sent to each backend (default 3000, sized for Apple's 4096-token model). Raise it for models with bigger context windows. For Ollama, also raise the server's `OLLAMA_CONTEXT_LENGTH`.
- `state_file` (default `state.json` next to the config) stores chats so they survive restarts. Set it to `"-"` to keep them only in memory.
- Environment variables override the file: `COURIER_BOT_TOKEN`, `COURIER_ALLOWED_USERS` (comma-separated), `COURIER_DEFAULT_MODEL`, `COURIER_CONFIG`.

## Keep it running (macOS)

Save this as `~/Library/LaunchAgents/com.iraosandeep.courier.plist`, fixing the binary path, then run `launchctl load ~/Library/LaunchAgents/com.iraosandeep.courier.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.iraosandeep.courier</string>
  <key>ProgramArguments</key><array><string>/Users/YOU/go/bin/courier</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardErrorPath</key><string>/tmp/courier.log</string>
</dict></plist>
```

## Security notes

- Anyone can find and message a Telegram bot. Courier answers only `allowed_users` and tells everyone else their user ID, so you can add them if you want to.
- Your messages pass through Telegram's servers (bot chats aren't end-to-end encrypted). The *model* runs locally, but the conversation itself isn't private from Telegram.
- The bot token never appears in logs or error messages.

## Development

```sh
make test      # unit tests with fake Telegram and model servers
make test-fm   # end-to-end against a running fm serve
make build
```
