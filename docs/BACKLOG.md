# Grok Bridge backlog

## Open

### BUG: Multi-hub / wrong Tailscale endpoint hides Build chats

- **Status:** open
- **Severity:** high (looks like feature regression; user sees empty Build rail)
- **Doc:** [MULTI_HUB.md](./MULTI_HUB.md)
- **GitHub:** https://github.com/Nomercy515/grok-bridge/issues/1

**Problem:** More than one `grok-bridge` can listen on `:4020` across Tailscale nodes. Bookmarks and `endpoint.json` can point at a hub that is (a) an older binary and/or (b) has no `GROK_HOME` / `~/.grok`, while Build chats only exist on another node. Updating “Bridge” on the Grok machine does not change what the phone already opens.

**Acceptance ideas (pick a thin slice first):**

1. **Endpoint identity:** `/api/endpoint` (and UI) show hostname, listen addr, git/version or build time, resolved `GrokHome()`, and Build session count (or “unavailable”).
2. **Stale-hub hygiene:** start/supervise warn if another Tailscale peer already advertises Bridge on 4020; document “one phone-facing hub”.
3. **Empty Build rail copy:** when `source=build` count is 0, show why (`GrokHome` path missing vs empty `sessions/`) instead of a silent bridge-only list.
4. **Optional:** prefer MagicDNS name bound to the intended machine; refresh `endpoint.json` on every listen.

**Non-goals for the bugfix:** inventing remote filesystem sync of `~/.grok` across machines.

## Done (recent)

- Env-agnostic Grok Build session list+open (read-only) — `6254bcc`
