# Headless UI checks

`live-status-queue.js` drives a running hub with Playwright (Chromium, headless).

```bash
npm install && npx playwright install chromium
# TOKFILE: a file that holds a paired bearer token (never printed by the script)
BASE=http://127.0.0.1:4020 TOKFILE=./token node live-status-queue.js
```

Environment:

- `BASE`: hub URL. Self-signed HTTPS is accepted.
- `TOKFILE`: path to a file with the bearer token.
- `OUT`: where screenshots go (default `./shots`).
- `SHOTS=0`: skip screenshots.
- `ONLY`: a comma list of `flow390,flow1280,flow360,flow414,missed,stop,reload`.

The script creates hub-native (`demo`) sessions titled "Queue demo …",
"Missed event" and so on. Run it against a hub started with
`GROK_BRIDGE_AGENT=demo GROK_BRIDGE_DEMO_DELAY_MS=150`, which makes replies slow
enough to queue behind. Against a real agent, `missed` / `stop` / `reload` also
work on demo sessions.
