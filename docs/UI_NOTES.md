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

- **Live pill** — orbit pulse when WebSocket is open
- **Sync / Offline** — degraded chips when polling or offline
- **Streaming** — caret + light shimmer on assistant bubble
- **Session rail** — active left hairline, relative time meta
- **Composer** — floating card; Enter send / Shift+Enter newline
- **Brand mark** — geometric orbit glyph (original)

## Accessibility

Dark greyscale contrast kept high on primary text; focus rings on interactive controls; pairing dialog uses `role="dialog"` + labelled title; status region `aria-live`.

## Tool cards (Phase 3)

- Live `tool_call` / `tool_result` (and `tool_card` alias) render as compact cards under the assistant bubble.
- Header: tool name + status chip (`running` / `done` / `error`).
- Arguments and result are `<details>` sections (collapsed by default once done; running opens args).
- Long JSON truncated client-side (~2400 chars) with a length note.
- Correlation: prefer `tool.id` / `call_id`; fall back to name + first open call.

## Cancel / Stop (Phase 4)

- **Stop** button next to Send; enabled only while a turn is active (after send / `assistant_start`, until `assistant_done`).
- Calls `POST /api/sessions/{id}/cancel` with the pairing bearer.
- Cancelled turns show a small **Cancelled** note on the assistant bubble (`assistant_done.cancelled` or persisted `message.cancelled`).
