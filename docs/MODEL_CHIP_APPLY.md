# Apply model selector on WSL (feat/new-session-build-acp)

## Blocker that forced this package
Remote Dev executor Shell with `machineId=47d3a8e9-…` did **not** route to DESKTOP-80GD1HQ; commands ran on the agent box. SSH to `100.95.136.80` refused. Only hub `:4020` is reachable. This tree is an apply-ready implementation + unit tests; it was **not** committed on WSL.

## On WSL
```bash
cd /home/eti_enne1/project/ZeroCPlus/grok-bridge
git checkout feat/new-session-build-acp
# Copy files from this package (or re-dispatch executor with working machine Shell):
#   internal/grokacp/config_options.go
#   internal/grokacp/config_options_test.go
#   internal/grokacp/client.go   (configOptions store + SetConfigOption + one-shot log)
#   internal/grokacp/events.go   (session_models WS event)
#   internal/hub/server.go       (GET …/models, PUT/POST …/model)
#   web/model-selector.js
#   web/model-selector.css
#   web/model-mock.css           (alias; hides leftover mock chrome)
#   web/index.html               (link model-selector.css/js instead of model-mock)
#   web/app.js                   (dispatch grok-bridge:session / grok-bridge:ws; window.GrokBridge)
# Soft-delete web/model-mock.js once selector is live (keep chip; remove mock switcher/bar).

export PATH="/home/eti_enne1/.local/go/bin:$PATH"
go test ./internal/grokacp/ ./internal/hub/ -count=1
go build -o bin/grok-bridge ./cmd/grok-bridge
touch data/restart.requested   # or systemctl restart as you usually do

# Verify (with bearer token):
curl -sk -H "Authorization: Bearer $TOKEN" \
  "https://100.95.136.80:4020/api/sessions/$BUILD_SID/models"
# Expect non-empty models[] from ACP — not hardcoded grok-4/claude/gpt mock list.
# Journal: grokacp: session/new configOptions for … should show category=model
```

## Do not
- Push or open PR (Valentine opens PR after live verify)
- Break tip fix: `session_created` → `refreshSessions()` only; tip listeners at init
