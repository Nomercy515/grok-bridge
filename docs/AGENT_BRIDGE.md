# Grok Bridge → agent bridge (design)

## Goal

Phone/desktop Grok Bridge sessions talk to a **Grok Bot agent** (not the demo backend) — with the same handoff, pairing, and Tailscale path.

## Constraint

A Grok Bot is not a local `agent serve` process. There is no public “stream chat to my bot” API. The bridge must **wake the bot** (typically a webhook routine), let it work with full tools/memory, then **push events back** into the hub.

## Architecture (MVP)

```
Phone / browser
    │  HTTPS + WSS (paired)
    ▼
Grok Bridge hub (default :4020)
    │  AgentBridge Adapter
    │  POST /bridge/v1/jobs  (shared secret)
    ▼
Webhook / relay
    │
    ▼
Grok Bot routine
    │  tools, memory, connectors
    │  POST hub /bridge/v1/jobs/{id}/events
    ▼
Hub fans out WS events to clients
```

### Components

1. **`AgentBridge` (`internal/bridge`)**  
   Implements `agent.Adapter.StreamReply`:
   - Creates a job `{session_id, message, history_ref, callback_base}`
   - POSTs to the webhook with `Authorization: Bearer <GROK_BRIDGE_AGENT_WEBHOOK_AUTH>`
   - Receives hub-inbound POSTs and yields them as `assistant_delta` / `tool_*` / `assistant_done`

2. **Hub inbound routes (bridge secret)**  
   - `POST /bridge/v1/jobs/{id}/events` — agent pushes deltas  
   - `POST /bridge/v1/jobs/{id}/complete` — finalizes turn  
   - `GET /bridge/v1/jobs/{id}` — agent pulls context (includes `status`)  
   - `POST /bridge/v1/jobs/{id}/cancel` — cancel mid-turn  
   - `POST /bridge/v1/jobs` — create job (machine)

3. **Relay / wake**  
   Preferred MVP: **webhook trigger** — hub POSTs to `GROK_BRIDGE_AGENT_WEBHOOK_URL`; the bot wakes, works, POSTs streaming chunks back over Tailscale/LAN.

4. **Secrets & URLs (never commit real values)**  
   - `GROK_BRIDGE_SECRET` — shared hub ↔ bot for **inbound** bridge routes (generated on install; gitignored)  
   - `GROK_BRIDGE_AGENT_WEBHOOK_URL` + `GROK_BRIDGE_AGENT_WEBHOOK_AUTH` — routine webhook URL and **sender key** for **outbound** POSTs (routine panel; do not publish)  
   - Hub pairing is for humans; bridge routes require the bridge secret (never expose to the phone UI)  
   - Callback URL = Tailscale MagicDNS of the hub (DHCP-resilient), including the same port you bind

## Session model

| Bridge | Agent |
|--------|-------|
| `session_id` | Job `remote_session_id`; optional mapping file |
| Transcript in `data/sessions/` | Hub is source of truth; agent may fetch via bridge GET |
| Multi-client fan-out | Unchanged — agent only feeds the hub |

## Phased delivery

| Phase | Ship |
|-------|------|
| **0** | Design + config knobs (`GROK_BRIDGE_AGENT=bot\|demo`) |
| **1** | Hub bridge routes + `AgentBridge` + `MockAgent` + tests |
| **2** | Real webhook routine that answers + posts deltas back |
| **3** | Tools visible in UI (map tool use → tool_card events) — **shipped** |
| **4** | Cancel / interrupt mid-turn — **shipped** |

## Phase 1 shipped

| Piece | Location |
|-------|----------|
| `AgentBridge` + `MockAgent` + `JobRegistry` | `internal/bridge` |
| Inbound routes | `/bridge/v1/jobs…` in `internal/hub` |
| Agent select | `GROK_BRIDGE_AGENT=demo\|bot` (default `demo`) |
| Tests | `go test ./...` |

### Modes

- **`GROK_BRIDGE_AGENT_MODE=mock`** (default): in-process `MockAgent` pushes streaming events into the job queue.
- **`GROK_BRIDGE_AGENT_MODE=webhook`**: POSTs the job payload to `GROK_BRIDGE_AGENT_WEBHOOK_URL`, then waits for inbound `/events` until timeout (`GROK_BRIDGE_AGENT_TIMEOUT`, default 120s).

### Auth

Bridge routes require **`GROK_BRIDGE_SECRET`** via `Authorization: Bearer …` or `X-Bridge-Secret`. The human pairing token alone is rejected (401).

### Run mock locally

```bash
export GROK_BRIDGE_SECRET="$(openssl rand -hex 24)"
export GROK_BRIDGE_AGENT=bot
export GROK_BRIDGE_AGENT_MODE=mock
./start.sh
```

## Phase 2 shipped (webhook agent)

### Wire-up

**Public repo rule:** document steps and env **names**. Never commit a real webhook URL, sender key, or `bridge.secret`.

1. Create a Grok Bot **webhook** routine whose prompt handles bridge jobs and POSTs events back to the hub.
2. From that routine’s panel, copy:
   - **Webhook URL** → `GROK_BRIDGE_AGENT_WEBHOOK_URL`
   - **Webhook key** / Authorization bearer → `GROK_BRIDGE_AGENT_WEBHOOK_AUTH`  
   Cursor requires `Authorization: Bearer <sender key>` on outbound POSTs.
3. On the hub host (example port **4020**):

```bash
export GROK_BRIDGE_AGENT=bot
export GROK_BRIDGE_AGENT_MODE=webhook
export GROK_BRIDGE_PORT=4020
export GROK_BRIDGE_AGENT_WEBHOOK_URL='<paste routine webhook URL>'
export GROK_BRIDGE_AGENT_WEBHOOK_AUTH='<paste routine sender key>'
export GROK_BRIDGE_SECRET="$(cat data/bridge.secret)"
export GROK_BRIDGE_CALLBACK_BASE='https://<machine>.<tailnet>.ts.net:4020'
./scripts/supervise.sh
```

4. Ensure the bot can read the same `GROK_BRIDGE_SECRET` when POSTing to `/bridge/v1/jobs/.../events` and `/complete`.
5. Send a message in the UI with agent=`bot` — hub POSTs the job; the routine wakes, streams `assistant_*` events, then `complete`.

### Payload (Phase 2)

```json
{
  "job_id": "<uuid>",
  "session_id": "<remote session>",
  "user_message": "...",
  "history_ref": "session:<id>:n=<count>",
  "history": [],
  "callback_base": "https://<magicdns>:<port>",
  "callback_hint": "/bridge/v1/jobs/<id>/events",
  "events_url": "https://<magicdns>:<port>/bridge/v1/jobs/<id>/events",
  "complete_url": "https://<magicdns>:<port>/bridge/v1/jobs/<id>/complete",
  "status_url": "https://<magicdns>:<port>/bridge/v1/jobs/<id>",
  "cancel_url": "https://<magicdns>:<port>/bridge/v1/jobs/<id>/cancel"
}
```

## Phase 3 shipped (tool cards)

See `docs/UI_NOTES.md`. Events: `tool_call` / `tool_result` / optional `tool_card` alias; match by `tool.id` / `call_id`.

## Phase 4 shipped (cancel / interrupt)

- `POST /api/sessions/{id}/cancel` (pairing bearer)
- `POST /bridge/v1/jobs/{id}/cancel` (bridge secret)
- UI **Stop** while streaming; WS `assistant_done` with `cancelled: true`
- Agent may `GET status_url` and stop if `status` is `cancelled`


## Grok Build ACP (live resume)

Bridge-native webhook/demo sessions are unchanged. For `build:<uuid>` sessions the hub talks to a local `grok agent serve` over WebSocket JSON-RPC (ACP):

1. `initialize`
2. `session/load` (raw UUID + `cwd` from `summary.json`)
3. `session/prompt` → stream `session/update` → Bridge `assistant_delta` / `tool_*` / `assistant_done`
4. Cancel → ACP `session/cancel` notification

Env: `GROK_BRIDGE_GROK_AGENT_WS`, `GROK_BRIDGE_GROK_AGENT_SECRET`, optional `GROK_BRIDGE_GROK_AGENT_AUTO_START=1`.
See `internal/grokacp` and `docs/grok-bridge.env.example`.
