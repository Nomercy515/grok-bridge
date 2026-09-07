# Multiple hubs / wrong endpoint (Build chats “missing”)

## Symptom

You open Bridge on a Tailscale URL (e.g. `http://100.x.x.x:4020`) and only see hub-native sessions. Grok Build chats that exist under `~/.grok/sessions` do not appear, even after the Build list+open feature shipped.

## What actually happened (2026-09-06)

Two independent hubs were both using port **4020** on different machines in the same Tailscale net:

| Hub | Bind | Data dir | Grok home | Build chats |
|-----|------|----------|-----------|-------------|
| Cursor box (old live E2E) | `100.122.75.47:4020` | `data/phase2-live` | unset / no `~/.grok` | **None** |
| WSL Ubuntu (updated binary) | `127.0.0.1` then `0.0.0.0:4020` | local `data/` | default `~/.grok` | **31+ sessions** |

Updating and testing the WSL tree fixed list+open **there**, but the bookmark / phone URL still pointed at the **box** Tailscale IP. That process was an older `/tmp/grok-bridge-4020` binary with no Build index and no readable Grok session store — so the UI looked “broken” while the WSL hub was fine.

WSL Tailscale address that served Build chats after rebind: `http://100.95.136.80:4020/`.

## Root causes (product bugs)

1. **No single source of truth for “the” hub endpoint.** `data/endpoint.json` / MagicDNS advertising can lag, and nothing stops a second process from binding `:4020` on another node.
2. **Build sessions are local to the process’s `GROK_HOME`.** Env-agnostic by design (`GROK_BRIDGE_GROK_HOME` → `GROK_HOME` → `~/.grok`). A hub on a machine without that tree correctly shows zero Build rows — easy to misread as a sync bug.
3. **Same port, different machines** makes Tailscale URLs look interchangeable when they are not (different binary, data dir, auth/pairing, and Grok home).

## What to do when chats are “missing”

1. Confirm which host you opened (`/api/endpoint`, hub log “listening on …”, Tailscale IP).
2. Confirm binary revision includes Build list+open.
3. Confirm that host can see Grok sessions: set `GROK_BRIDGE_GROK_HOME` (or `GROK_HOME`) to the real `.grok` directory **on that same host**, or run the hub where Grok Build runs.
4. Avoid leaving stale hubs on `:4020` on other nodes.

## Backlog

See [BACKLOG.md](./BACKLOG.md) and the linked GitHub issue for hardening work (single-endpoint discovery, stale-hub warnings, clearer empty-Build UX).
