# Model chip handoff (composer)

## What shipped

Composer **model chip** for Build sessions (`build:…`), fed by ACP `configOptions` (category `model`), not a hard-coded list.

### Hub API

| Method | Path | Behavior |
|--------|------|----------|
| `GET` | `/api/sessions/{id}/models` | Build session: `ModelState` JSON, or `{ "available": false, "reason": "…" }`. Non-build → `available: false`. |
| `POST` | `/api/sessions/{id}/model` | Body `{ "value": "<option value>" }`. Calls ACP `session/set_config_option`, updates cache, returns `ModelState`. `400` missing value; `404` unknown session; `501` no model config / legacy-only. |

**ModelState shape**

```json
{
  "config_id": "model",
  "current": "model-fast",
  "options": [
    { "value": "model-fast", "name": "Fast", "description": "…" }
  ]
}
```

Parsing prefers `configOptions[]` with `category == "model"` (and `type == "select"`). If both exist, **configOptions wins** over legacy `models.currentModelId` + `models.availableModels`. Grouped select options are flattened.

### ACP client (`internal/grokacp`)

- `NewSession` → `(sessionID, *ModelState, error)`
- `LoadSession` → `(*ModelState, error)` (cached state if same cwd already loaded)
- `SetConfigOption(ctx, sessionID, configID, value)` → `session/set_config_option` with `type: "id"`
- Unit tests cover configOptions, legacy, set_config_option (mocked JSON-RPC)

### Hub (`internal/hub`)

- Caches `buildModels` per `build:…` id on create / load / set
- Routes wired under existing `/api/sessions/` handler
- Integration test `TestSessionModelsAPI` with fake ACP

### UI

- Pale chip between textarea and Send (`web/index.html` + `web/styles.css` + `web/app.js`)
- Menu opens upward; Escape / outside click closes (same pattern as usage tip)
- Hidden when non-build, demo mode, or `available: false` / empty options
- Labels: `name` with fallback to `value`
- Also fixes misplaced usage-tip listeners that had been nested inside `session_created`

## Files touched

- `internal/grokacp/models.go` (new)
- `internal/grokacp/models_test.go` (new)
- `internal/grokacp/client.go`
- `internal/grokacp/client_test.go`
- `internal/hub/server.go`
- `internal/hub/hub_test.go`
- `web/index.html`, `web/app.js`, `web/styles.css`
- `MODEL_CHIP_HANDOFF.md` (this file)

## Verify on live hub (WSL / machine with Grok agent)

1. Pair the Bridge UI as usual.
2. Ensure `grok agent serve` is reachable (or `GROK_BRIDGE_GROK_AGENT_AUTO_START=1`).
3. **New Build session** from the UI (not demo).
4. Confirm the pale chip appears left of Send with the **real** current model name.
5. Open the menu — options should match the agent’s advertised models (not a fake Grok/Claude/GPT list).
6. Pick another model; chip label updates; send a message and confirm the agent uses the new model (agent-side).
7. Optional curl (replace token / id):

```bash
curl -sS -H "Authorization: Bearer $TOKEN" \
  "$HUB/api/sessions/build%3A$RAW/models" | jq .
curl -sS -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"value":"YOUR_MODEL_ID"}' \
  "$HUB/api/sessions/build%3A$RAW/model" | jq .
```

## Remaining WSL deploy steps (Valentine)

1. Pull / cherry-pick this commit onto the WSL clone (branch `feat/new-session-build-acp`).
2. `go test ./internal/grokacp/ ./internal/hub/`
3. `node --check web/app.js`
4. Restart the hub against a real Grok agent; run the live checks above.
5. **Then** open the PR (do not push from the box unless live-verified).

## Could not verify here

This environment has **no real Grok agent**. Behavior was verified with mocked ACP JSON-RPC + hub integration tests only.
