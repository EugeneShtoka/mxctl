# mxctl

**mxctl** is a Matrix sync daemon that receives messages and forwards them to plugins. It does nothing else.

Notifications, clipboard, filtering, routing — none of that lives here. mxctl is the delivery layer. Plugins own the behaviour.

## Philosophy

Most messaging tools conflate reception with action. mxctl separates them: one process listens to Matrix and pipes events to whatever you want. This keeps the core small and auditable, and lets you compose behaviour freely — shell scripts, Go binaries, Python, anything that reads stdin.

The plugin interface is deliberately simple: newline-delimited JSON over stdin. No shared libraries, no build-time coupling, no framework. A plugin that works today will work after any mxctl update, as long as the protocol version matches.

Stateless by design — no database, no persistent state. On each start mxctl positions itself at the current moment and listens forward only.

## Install

```sh
go install github.com/EugeneShtoka/mxctl@latest
```

Or from source:

```sh
git clone https://github.com/EugeneShtoka/mxctl
cd mxctl
go build -o ~/.local/bin/mxctl .
```

## Usage

**Login:**
```sh
mxctl login
mxctl login --homeserver https://matrix.example.com --user @you:example.com
```

**Start syncing:**
```sh
mxctl sync
```

**As a systemd user service:**
```ini
# ~/.config/systemd/user/mxctl-sync.service
[Unit]
Description=mxctl Matrix sync daemon

[Service]
ExecStart=%h/.local/bin/mxctl sync
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
```

```sh
systemctl --user enable --now mxctl-sync
```

## Config

`~/.config/mxctl/config.json` — created by `mxctl login`. Override the directory with `MXCTL_CONFIG_DIR`.

```json
{
  "homeserver":   "https://matrix.example.com",
  "user_id":      "@you:example.com",
  "access_token": "...",
  "device_id":    "...",

  "aliases": {
    "@you:example.com": {"name": "You", "severity": "low", "color": "#888888"}
  },

  "room_aliases": {
    "!abc123:example.com": {"name": "Work", "severity": "high", "color": "#ff4444"}
  },

  "plugins": [
    {
      "name": "code-to-clipboard",
      "pipes": [
        {"cmd": "~/.local/bin/clipkit",    "config": {"extract_code": true}},
        {"cmd": "wl-copy"}
      ],
      "terminating": true
    },
    {
      "name": "url-to-clipboard",
      "pipes": [
        {"cmd": "~/.local/bin/clipkit",    "config": {"extract_url": true}},
        {"cmd": "wl-copy"}
      ],
      "terminating": true
    },
    {
      "name": "notify",
      "pipes": [
        {"cmd": "~/.local/bin/mxctl-notify"}
      ]
    }
  ]
}
```

### Aliases

Aliases let you assign a display name, severity, and color to any Matrix user ID. These values are resolved before the event reaches plugins — `sender_name` in the event payload will contain the alias name if one is defined.

```json
"aliases": {
  "@alice:example.com":  {"name": "Alice", "severity": "normal", "color": "#4a9eff"},
  "@bot:example.com":    {"name": "Bot",   "severity": "low"}
}
```

`severity` has four defined levels: `low`, `normal`, `high`, `critical`. `color` is a free-form string (e.g. a hex color). Both are optional and omitted from the event if not set. Their interpretation is left to plugins.

### Room aliases

Room aliases work the same way for rooms: assign a display name, severity, and color to any Matrix room ID.

```json
"room_aliases": {
  "!abc123:example.com": {"name": "Work",   "severity": "high", "color": "#ff4444"},
  "!xyz789:example.com": {"name": "Friends", "severity": "low"}
}
```

Room alias `name` overrides the Matrix room display name. For `severity`, the higher of the sender alias and room alias values is used (`critical` > `high` > `normal` > `low`). For `color`, sender alias takes precedence; room alias is the fallback.

## Plugin Interface

Plugins are **spawned per event** — mxctl forks each pipe in the chain for every incoming message, pipes data through, and the process exits. No long-lived processes, no idle resource usage.

### Pipe chain

Each plugin defines a `pipes` array. mxctl runs them in order:

1. Step 0 receives the message body on stdin.
2. Each subsequent step receives the previous step's stdout on stdin.
3. If any step exits non-zero, the chain aborts and mxctl moves to the next plugin.
4. If all steps succeed and the plugin is `"terminating": true`, mxctl stops processing further plugins for this event.

`terminating` only triggers when the **last pipe exits 0**. Any earlier failure is non-terminating — mxctl always continues to the next plugin.

### Invocation

Each pipe is invoked as:

```sh
cmd --config '{"key":"value"}' --event '{"event_id":"...","body":"hello",...}'
```

| Source | Content |
|---|---|
| stdin | Accumulated JSON object. Step 0 receives the full event JSON. Each subsequent step receives the event merged with all previous pipe outputs — pipe output wins, event fills missing fields. |
| `--config` | JSON config object from `config.json` for this pipe (omitted if none) |
| `--event` | Original Matrix event JSON, immutable, unchanged throughout the chain |

Pipes output only the fields they change or add. Custom fields are allowed and propagate to downstream pipes. Exit 0 = success, continue chain. Exit non-zero = abort chain.

### Event JSON (`--event`)

```json
{
  "event_id":    "$abc123",
  "room_id":     "!xyz:example.com",
  "room_name":   "Alice",
  "sender":      "@alice:example.com",
  "sender_name": "Alice",
  "body":        "hello",
  "msg_type":    "m.text",
  "ts":          1713600000000,
  "severity":    "normal",
  "color":       "#4a9eff"
}
```

| Field | Description |
|---|---|
| `event_id` | Matrix event ID |
| `room_id` | Matrix room ID |
| `room_name` | Resolved room display name |
| `sender` | Raw Matrix user ID |
| `sender_name` | Resolved display name (alias takes precedence over Matrix profile) |
| `body` | Original message text |
| `msg_type` | Matrix message type (`m.text`, `m.image`, etc.) |
| `ts` | Timestamp, Unix milliseconds |
| `severity` | From alias config — absent if not set |
| `color` | From alias config — absent if not set |

### Minimal pipe example (shell)

```sh
#!/bin/sh
# Reads accumulated JSON from stdin, sends a notification, exits 0.
input=$(cat)
body=$(printf '%s' "$input"   | jq -r '.body')
sender=$(printf '%s' "$input" | jq -r '.sender_name')
notify-send "$sender" "$body"
```

A pipe that transforms the body and passes it forward:

```sh
#!/bin/sh
input=$(cat)
body=$(printf '%s' "$input" | jq -r '.body' | tr '[:lower:]' '[:upper:]')
printf '%s' "$input" | jq --arg b "$body" '.body = $b'
```

### Available plugins

| Plugin | Description |
|---|---|
| [mxctl-notify](https://github.com/EugeneShtoka/mxctl-notify) | Desktop notifications via `notify-send` |

## Requirements

- Go 1.21+
- Linux (plugins typically call `notify-send` or similar)
