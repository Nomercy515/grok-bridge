# Grok Bridge

<p align="center">
  <img src="brand/logo.png" alt="Grok Bridge — bridge arches as the bot’s eyes" width="160" height="160" />
</p>

**Grok Bridge** fills a gap Claude’s world already covers: keep a **Grok Build** session alive on your machine and keep working it from somewhere else — couch, commute, another desk — while you’re away from the keyboard that started it.

Run a small hub on a desktop, VM, or always-on box. Pair from any browser on your [Tailscale](https://tailscale.com) network (phone or laptop). Same conversation, live streaming replies, optional Grok Bot agent on the backend with tools and memory. No public port-forward — MagicDNS only.

### Compared to Claude Code Remote Control

[Claude Code Remote Control](https://code.claude.com/docs/en/remote-control) lets you steer a **local Claude Code** session from the Claude app / `claude.ai/code`. The model still runs on your machine; Anthropic’s cloud is the remote window (outbound relay, vendor UI).

| | **Claude Code Remote Control** | **Grok Bridge** |
|--|--------------------------------|-----------------|
| Job | Continue a Claude Code CLI/IDE session from phone or browser | Continue a **Grok Build** / Grok Bot chat session from phone or browser |
| Where work runs | Your machine (Claude Code stays local) | Your machine (hub + optional Grok Bot agent stay on your side) |
| How you connect | Anthropic relay + Claude app / claude.ai | **Your** Tailscale tailnet + open web UI on the hub |
| Transport | Vendor-hosted remote control (no inbound ports on your LAN) | Self-hosted hub on MagicDNS / `100.x` (you own the path; keep `:4020` off the public internet) |
| Backend | Claude Code on the desk | Demo agent, or a **Grok Bot** woken over webhook with stream-back, tool cards, and Stop |
| Feel | Official remote for Claude’s coding loop | Community-shaped remote for Grok’s loop — inspired by [Amnibro/grok-remote](https://github.com/Amnibro/grok-remote) (MIT), clean-room Go rewrite |

If Remote Control is “leave the desk, keep Claude Code,” Grok Bridge is “leave the desk, keep **Grok Build**” — same idea, Grok-shaped, Tailscale-native, and yours to host.

Default listen port: **4020**. Canonical phone URL:

`https://<machine>.<tailnet>.ts.net:4020/`

**Do not** expose `:4020` to the public internet — Tailscale only.

## What you get

- **Desktop ↔ phone session bridge** — one server-authoritative transcript under `data/sessions/`; pick up the same conversation on another Tailscale device
- **Live WebSocket chat** — streaming assistant replies with fan-out to every connected client; `session.snapshot` resume after reconnect
- **Pairing lock** — 6-digit code → hub bearer token; code rotates after each successful pair; token rotate without re-pairing
- **Mobile-friendly UI** — dark greyscale cockpit, session rail, floating composer, live/sync/offline status (see `docs/UI_NOTES.md`)
- **HTTP fallback when WS drops** — `POST /api/sessions/{id}/messages`, background catch-up poll, reconnect backoff
- **Tailscale-first networking** — MagicDNS + `100.x` survive LAN DHCP churn; `GET /api/endpoint` + sidebar show the bookmark URL
- **TLS / WSS** — optional certs; install can mint self-signed SANs for localhost + MagicDNS + Tailscale IP
- **Supervised install** — `scripts/supervise.sh` + systemd (`grok-bridge-endpoint` then `grok-bridge`) so the hub comes back after reboot
- **Demo agent** — `/?demo=1` or `GROK_BRIDGE_AGENT=demo` for canned streams while you wire the rest
- **Grok Bot agent bridge (Phases 1–4)** — pluggable backend:
  - **Mock** mode for local streaming without a real bot
  - **Webhook** mode to wake a Grok Bot routine, then stream deltas back into the hub
  - **Tool cards** — live `tool_call` / `tool_result` (and `tool_card`) with running/done/error chips
  - **Cancel / Stop** — stop a mid-turn reply from the UI or API; job status becomes `cancelled`
- **Machine bridge API** — shared `GROK_BRIDGE_SECRET` for job create / events / complete / cancel (separate from human pairing)
- **No secrets in the repo** — bridge secret and pairing material are generated at install into gitignored `data/`

## Architecture

```
Phone / other Tailscale device
    │  HTTPS + WSS (paired)
    ▼
Grok Bridge hub (:4020)  ←── MagicDNS / 100.x
    │  AgentAdapter
    ├── demo (canned)
    └── bot → mock  or  webhook → Grok Bot routine
                 │                    │
                 │                    └── POST /bridge/v1/jobs/…/events|complete
                 └── in-process stream
```

Boot order on an installed host:

```
tailscaled → grok-bridge-endpoint (endpoint.json + TLS SANs) → grok-bridge (supervise.sh)
```

## Quick start (local)

```bash
git clone <this-repo> && cd grok-bridge
go build -o bin/grok-bridge ./cmd/grok-bridge
./start.sh
# Open http://127.0.0.1:4020/?demo=1
# Pairing code is printed in the terminal on first start
```

Supervised (recommended):

```bash
./scripts/supervise.sh
```

## Install (Linux VM / host)

```bash
cd /path/to/grok-bridge
./scripts/install.sh
# Optional non-interactive Tailscale auth:
# TAILSCALE_AUTHKEY=tskey-auth-… ./scripts/install.sh
```

| Step | Action |
|------|--------|
| Go | Requires Go ≥ 1.22; builds `bin/grok-bridge` |
| Scripts | `chmod +x` start / supervise / gen-dev-certs / refresh-endpoint |
| Secrets | Generates `GROK_BRIDGE_SECRET` into the local data dir (gitignored). Pairing material is created on first hub start into `data/auth.json` (mode **0600**) |
| Tailscale | Official installer on debian/ubuntu; `systemctl enable --now tailscaled`; `tailscale up` (or `TAILSCALE_AUTHKEY`) |
| Endpoint | Runs `scripts/refresh-endpoint.sh` → `data/endpoint.json` + TLS SANs for MagicDNS + `100.x` |
| systemd | Installs `grok-bridge-endpoint.service` (oneshot) then `grok-bridge.service` (supervise) |

```bash
journalctl -u grok-bridge -e
./scripts/refresh-endpoint.sh
sudo systemctl restart grok-bridge-endpoint grok-bridge
```

Flags: `SKIP_SYSTEMD=1`, `SKIP_TAILSCALE=1`.

## Why Tailscale (only)

VM LAN addresses often change on every DHCP lease. **MagicDNS** and Tailscale `100.x` stay stable.

- Same tailnet on the hub host and the phone (or other client); MagicDNS enabled in the admin console
- Bookmark the MagicDNS HTTPS URL from `data/endpoint.json`, the UI sidebar, or `GET /api/endpoint`
- Keep `:4020` off the public internet

This project does not invent other mesh, tunnel, or LAN auto-discovery paths.

## Environment variables

| Setting | Default | Env |
|---------|---------|-----|
| Host | `127.0.0.1` | `GROK_BRIDGE_HOST` (install prefers Tailscale `100.x`, else `127.0.0.1`; `0.0.0.0` only if set explicitly) |
| Port | `4020` | `GROK_BRIDGE_PORT` (keep callback URL on the same port) |
| Data | `./data` | `GROK_BRIDGE_DATA` |
| TLS cert / key | off | `GROK_BRIDGE_SSL_CERT` / `GROK_BRIDGE_SSL_KEY` |
| Agent | `demo` | `GROK_BRIDGE_AGENT` (`demo` \| `bot`) |
| Bridge secret | — | `GROK_BRIDGE_SECRET` (required for `bot`; **generated on install**; inbound callbacks) |
| Agent mode | `mock` | `GROK_BRIDGE_AGENT_MODE` (`mock` \| `webhook`) |
| Agent webhook URL | — | `GROK_BRIDGE_AGENT_WEBHOOK_URL` (paste from routine panel — **do not commit**) |
| Agent webhook key | — | `GROK_BRIDGE_AGENT_WEBHOOK_AUTH` (sender key for outbound Bearer — **do not commit**) |
| Callback base | MagicDNS from `endpoint.json` | `GROK_BRIDGE_CALLBACK_BASE` (reachable from the bot; include port) |
| Agent timeout | `120s` | `GROK_BRIDGE_AGENT_TIMEOUT` |
| Allow `?token=` | off | `GROK_BRIDGE_ALLOW_QUERY_TOKEN=1` (discouraged) |

Never commit real secrets, webhook URLs, or sender keys. See `docs/AGENT_BRIDGE.md` and `docs/grok-bridge.env.example`.

## Security

| Control | Behavior |
|---------|----------|
| Bind | Default `127.0.0.1`. Install prefers Tailscale `100.x`; **`0.0.0.0` only if set explicitly**. No public port-forward |
| TLS | Optional HTTPS/WSS; boot refresh keeps SANs = localhost + MagicDNS + `100.x`. Prefer mkcert / Tailscale HTTPS on hostile networks |
| Pairing | Rate-limited (5/60s per IP); constant-time code compare; code rotates after each success |
| Auth | Bearer on REST; WS uses `Sec-WebSocket-Protocol: grok.bearer.<token>` only |
| Secrets | `data/auth.json` and bridge secret are mode **0600** / gitignored — **not** in the repo |
| Rotate | `POST /api/auth/rotate` (token), `POST /api/auth/rotate-pairing` (code) |
| Restart | `POST /api/control/restart` (bearer) for supervised re-exec |
| Tailscale install | `curl\|bash` from tailscale.com — residual supply-chain risk; use `SKIP_TAILSCALE=1` + distro packages when needed |

### Dev TLS certs

```bash
./scripts/gen-dev-certs.sh
./scripts/gen-dev-certs.sh 100.x.y.z myhost.tailnet.ts.net
```

## API (MVP)

| Method | Path | Auth | Notes |
|--------|------|------|--------|
| GET | `/health` | no | `{"ok":true}` |
| GET | `/api/endpoint` | no | MagicDNS / `100.x` URL |
| GET | `/api/auth/status` | no | pairing + nested `endpoint` |
| POST | `/api/auth/pair` | no | `{"code"}` → token |
| POST | `/api/auth/rotate` | bearer | new token |
| POST | `/api/auth/rotate-pairing` | bearer | new pairing code |
| GET/POST | `/api/sessions` | bearer | list / create |
| GET | `/api/sessions/{id}` | bearer | full transcript |
| POST | `/api/sessions/{id}/messages` | bearer | HTTP chat fallback |
| POST | `/api/sessions/{id}/cancel` | bearer | Stop mid-turn |
| POST | `/api/control/restart` | bearer | supervised restart |
| WS | `/ws` | bearer | live events |
| POST | `/bridge/v1/jobs` | bridge secret | create job |
| GET | `/bridge/v1/jobs/{id}` | bridge secret | job + context (`cancelled` possible) |
| POST | `/bridge/v1/jobs/{id}/events` | bridge secret | push `assistant_*` / `tool_*` |
| POST | `/bridge/v1/jobs/{id}/complete` | bridge secret | mark done |
| POST | `/bridge/v1/jobs/{id}/cancel` | bridge secret | cancel job |

Bridge auth: `Authorization: Bearer <GROK_BRIDGE_SECRET>` or `X-Bridge-Secret` (not the human pairing token).

## Agent bridge (quick)

```bash
export GROK_BRIDGE_AGENT=bot
export GROK_BRIDGE_AGENT_MODE=mock   # or webhook + URL/AUTH from the routine panel
export GROK_BRIDGE_SECRET="$(openssl rand -hex 24)"
./start.sh
```

Full wire-up (webhook, tool cards, cancel): `docs/AGENT_BRIDGE.md`.

## Tests

```bash
go test ./...
go build -o bin/grok-bridge ./cmd/grok-bridge
```

## Contributing

- Keep the hub Tailscale/LAN-oriented; do not add public hosting defaults
- No secrets in commits (`data/`, `certs/`, `*.pem`, env files are gitignored)
- Prefer `go test ./...` green before PRs
- Agent backends implement `internal/agent.Adapter`
- UI notes: `docs/UI_NOTES.md`

## License

MIT — see `LICENSE`.
