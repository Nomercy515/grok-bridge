# Live turn status and message queueing

## What changed for the user

- **Send never locks.** While Grok is replying, the composer shows **Stop** next
  to the Send button. The Send button reads **Queue**. Enter still sends.
- **Typing during a reply queues the message.** Each queued message shows up as
  a numbered chip above the composer. Use **Edit** to change it before it goes
  out (Enter saves, Esc cancels) or **×** to remove it.
- **The queue drains automatically.** When a reply finishes normally, the hub
  sends the next queued message right away, oldest first.
- **Stop ends only the current reply.** Queued messages are kept, and the queue
  **pauses** ("Paused after Stop"), so a stopped turn never fires the next
  prompt on its own. Tap **Send queued now** to resume. A turn that fails (agent
  down, for example) pauses the queue the same way.
- **The queue lives on the hub.** It survives a page refresh, switching devices
  and hub restarts. After a restart it comes back paused, because nothing is
  running any more.
- **Stop never sticks.** The UI no longer trusts the WebSocket event stream alone
  to decide whether a reply is running (see below).

## Root cause of the stuck Stop button

Before this change, `web/app.js` kept a local `turnActive` flag. Only these set
it:

- `assistant_start`, or
- a local send.

Only these cleared it:

- `assistant_done` / `error` over the WebSocket, or
- opening a session.

The client never asked the hub. So any missed `assistant_done` left Stop on
screen forever. Causes included:

- a socket drop or reconnect mid-turn,
- a phone asleep,
- a background tab whose socket was reaped,
- a proxy hiccup.

The 4s catch-up poll even rendered the finished reply, but it never touched
`turnActive`. The same single button doubled as Send and Stop, and both
`sendMessage` and the Enter handler bailed out while `turnActive` was set. That
is why sending was impossible while a turn was (or looked) active.

A headless repro on the old code, dropping only the `assistant_done` frame,
ended with `btnSend` stuck as "Stop" after the reply had fully rendered. Enter
did nothing.

## Design

### Hub (`internal/hub/turnstate.go`)

`turnTracker` is the single source of truth for each session. It holds:

- `state` (`idle` | `running`), `turn_id`, `started_at`,
- `updated_at`, bumped on every streamed `assistant_*` / `tool_*` event,
- `last_turn_id`, `last_result` (`done` | `cancelled` | `error`), `last_ended_at`,
- `queue[]` (FIFO, `{id, content, created_at, updated_at}`) and `queue_paused`.

How messages flow:

- Every user message, from WS `chat.send`, `POST /messages` or `POST /queue`,
  goes through `submitMessage`.
- If the session is idle and nothing is queued, the session is marked running
  **before** the turn goroutine starts. Otherwise the message is appended to the
  queue.
- `turnLoop` runs the turn, then calls `Finish(result)`. That either pops the
  next item (and marks the new turn running in the same critical section, so
  status never flickers to idle between queued turns) or goes idle.
- `cancelled` / `error` results pause a non-empty queue.

Persistence: each queue is stored in `<data>/queues/<session>.json` with mode
0600, written atomically. The file is removed when the queue is empty. Limits
are 50 items per session and 64 KiB per message.

Every change is broadcast as WS `{type:"turn_status", session_id, status}`.
`session.snapshot` replies to `session.subscribe` now carry `status` too, so
every (re)connect gets the truth immediately.

All direct WS replies now go through `Hub.sendTo`, which shares the broadcast
lock. gorilla/websocket allows only one concurrent writer per connection.

### API (bearer auth, like the rest of `/api/*`)

| Method | Path | Result |
|--------|------|--------|
| GET | `/api/sessions/{id}/status` | the status object above |
| POST | `/api/sessions/{id}/messages` | `200 {message_id}` when started; `202 {queued:true, queue_item, status}` when queued |
| GET / POST | `/api/sessions/{id}/queue` | status / enqueue `{content}` (starts immediately when idle and empty) |
| PUT or PATCH | `/api/sessions/{id}/queue/{qid}` | edit `{content}` |
| DELETE | `/api/sessions/{id}/queue/{qid}` | remove |
| POST | `/api/sessions/{id}/queue/send` | un-pause; if idle, start the next item now |
| POST | `/api/sessions/{id}/cancel` | stop the current turn only; response includes `status` |

WS `chat.send` that gets queued answers the sender with
`{type:"queued", queue_item, content, status}`, so the client can drop its
optimistic bubble.

### Client (`web/app.js`)

- `applyStatus(st)` is the only thing that decides Stop vs no Stop, the chips
  and the paused note. When the hub says idle but the UI thought a turn was
  running, it clears Stop, closes any half-streamed bubble and fetches the
  missed messages (`catchUp`).
- The client reconciles with `GET /status` in all of these cases:
  - WS (re)connect, via the snapshot status plus an explicit fetch,
  - `visibilitychange` to visible, `focus`, `pageshow`, `online`,
  - opening a session, and boot,
  - `assistant_done` / `error`,
  - a ~3s poll that runs **only while a turn is running**. On failures it backs
    off exponentially up to 30s, and it slows to 30s while the tab is hidden.
- Send is never disabled by a running turn. If a turn is running, or messages
  are already queued, Send posts to `/queue`; otherwise it starts a turn as
  before.
- Back-to-back turns exposed two older client races. Both are fixed:
  - A disk reply could be "adopted" into the next turn's live bubble. The live
    bubble is now adopted only when its text is a prefix of the disk text.
  - Tool cards didn't keep a bottom-pinned feed pinned.

## Tests

- `go test ./internal/hub -run 'Status|Queue|WSSnapshot'` covers:
  - the status endpoint and auth on every new route,
  - FIFO order and drain-on-idle,
  - Stop keeping (and pausing) the queue, plus Send-queued-now,
  - edit / remove / persistence across a simulated hub restart,
  - WS snapshot status and the `queued` ack.
- `scripts/e2e/live-status-queue.js` (Playwright, headless) covers:
  - Stop clears after dropped `assistant_done` + `turn_status` frames,
  - Send stays enabled while running,
  - queue chips, edit, auto-drain, Stop + Send queued now, reload keeping the queue,
  - layout checks at 360/390/414/1280: no horizontal scroll, tap targets ≥44px,
    controls inside the viewport.
