# Grok Bridge — UI design notes

Product chrome for the hub web client.

## Tokens

| Token | Value | Role |
|-------|-------|------|
| `--bg` | `#050505` | Page ground |
| `--bg-elev` | `#0c0c0c` | Rail / composer / sheets |
| `--bg-soft` | `#121212` | Hover / soft fills |
| `--border` | `#27272a` | Zinc hairlines |
| `--text` | `#f4f4f5` | Primary copy |
| `--muted` / `--muted-2` | `#a1a1aa` / `#71717a` | Secondary |
| `--accent` | `#e4e4e7` | Cool near-white actions |
| `--stage-max` | `760px` | Chat column |
| `--font` | Inter + system sans | UI |
| `--mono` | system mono | Tool cards |

## Affordances

- **Context meter** — topbar `#ctxMeter` shows active session context (`311k / 500k 62%`); tooltip has exact tokens + %. Build `signals.json` fields; `—` when unknown. Live/hub status pills removed (connection health via insecure/degraded banners only).
- **Streaming** — caret + light shimmer on assistant bubble
- **Session rail** — active left hairline, relative time meta
- **Composer** — floating card; Enter send / Shift+Enter newline; single primary button morphs Send ↔ Stop
- **Brand mark** — geometric orbit glyph (original)


## Message formatting

Chat bodies use `formatMessageHTML` / `setMsgBody` (not raw `textContent`):

- **Bold** — complete `**pairs**` only become `<strong>`; unmatched `**` stay literal. Escape HTML before bold so user markup cannot inject tags.
- **System reminders** — complete `<system-reminder>…</system-reminder>` blocks (case-insensitive) render as a muted `.sys-reminder` aside with a “System reminder” label (not as Grok answer text). Inner text still gets escape + bold.
- **User query** — complete `<user_query>…</user_query>` blocks are unwrapped into the normal You/message body (inner text only; no labeled aside). Build history load prefers the unwrapped query as `Message.Content` and skips reminder-only synthetic user lines.
- Streaming keeps `data-raw` on `.body` and re-formats on each delta / `assistant_done` / merge.

## Accessibility

Dark greyscale contrast kept high on primary text; focus rings on interactive controls; pairing dialog uses `role="dialog"` + labelled title; status region `aria-live`.

## Tool cards (Phase 3)

- Live `tool_call` / `tool_result` (and `tool_card` alias) render as compact cards under the assistant bubble.
- Header: tool name + status chip (`running` / `done` / `error`).
- Arguments and result are `<details>` sections (collapsed by default once done; running opens args).
- Long JSON truncated client-side (~2400 chars) with a length note.
- Correlation: prefer `tool.id` / `call_id`; fall back to name + first open call.

## Cancel / Stop (Phase 4)

- One primary composer button (`#btnSend`): **Send** when idle; morphs to **Stop** while a turn is active (`turnActive` after send / `assistant_start`, until `assistant_done`). Never show both.
- Stop click calls `POST /api/sessions/{id}/cancel` with the pairing bearer; `aria-label` / title switch with the mode.
- Cancelled turns show a small **Cancelled** note on the assistant bubble (`assistant_done.cancelled` or persisted `message.cancelled`).

## Grok Build sessions (live via ACP)

The session rail merges hub-native chats (`source: "bridge"`) with on-disk Grok Build sessions (`source: "build"`, ids `build:<session-id>`).

- Point the hub at a Grok home via `GROK_BRIDGE_GROK_HOME` (preferred) or `GROK_HOME`, else `~/.grok`.
- Expected layout: `$GROK_HOME/sessions/<url.PathEscape(cwd)>/<session-id>/summary.json` + `chat_history.jsonl` (flat `sessions/<id>/` also tolerated).
- Opening a Build session loads history from disk; the composer stays enabled. Sending resumes the real session over Grok ACP (`session/load` → `session/prompt`) and streams `assistant_delta` / tool events back on the Bridge WebSocket.
- Status chip: **via Grok Build (ACP)**. Badge **Build** remains.
- If `grok agent serve` is unreachable, send returns a clear 503 / UI error (not a silent read-only lock). Configure `GROK_BRIDGE_GROK_AGENT_WS` + `GROK_BRIDGE_GROK_AGENT_SECRET`, or set `GROK_BRIDGE_GROK_AGENT_AUTO_START=1`.
- Missing Grok home → empty Build list; hub-native sessions unchanged.

## Context meter

Header readout for the **active** chat session window usage (`#ctxMeter`). Build sessions populate `context_tokens_used`, `context_window_tokens`, and `context_window_usage` from `signals.json` beside `summary.json` when `Get` loads the session. Hub-native sessions omit these until known (UI shows `—`).
