# Grok Bridge backlog

## Open

_(none — multi-hub identity slice shipped; see Done)_

## Done (recent)

### BUG: Multi-hub / wrong Tailscale endpoint hides Build chats

- **Status:** fixed (thin slice) — see PR that closes issue #1
- **Doc:** [MULTI_HUB.md](./MULTI_HUB.md)
- **GitHub:** https://github.com/Nomercy515/grok-bridge/issues/1

Shipped: `/api/endpoint` + UI hub identity (host, listen, version, Grok home, Build count/unavailable), empty Build rail copy, supervise peer warning + endpoint refresh on listen. Non-goal unchanged: no remote `~/.grok` sync.

- Grok Build live send/receive via ACP (`session/load` + `session/prompt`, streaming) — composer unlocked; env-agnostic agent WS
- Env-agnostic Grok Build session list+open (read-only) — `6254bcc`
