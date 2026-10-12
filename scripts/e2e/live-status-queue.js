// Headless checks for live status + queueing against a running bridge.
// BASE=https://host:port TOKFILE=path node e2e.js   (token never printed)
const { chromium } = require('playwright');
const fs = require('fs');
const BASE = process.env.BASE, TOK = fs.readFileSync(process.env.TOKFILE, 'utf8').trim();
const OUT = process.env.OUT || './shots';
fs.mkdirSync(OUT, { recursive: true });
const SHOTS = process.env.SHOTS !== '0';
const results = [];
const ok = (name, cond, extra) => { results.push({ name, pass: !!cond, ...(extra ? { extra } : {}) }); };

async function api(ctx, method, path, body) {
  const r = await ctx.request.fetch(BASE + path, { method, headers: { Authorization: 'Bearer ' + TOK, 'Content-Type': 'application/json' }, data: body ? JSON.stringify(body) : undefined, ignoreHTTPSErrors: true });
  return { status: r.status(), body: await r.json().catch(() => ({})) };
}
async function newSess(ctx, title) {
  const r = await api(ctx, 'POST', '/api/sessions', { title, demo: true });
  if (!r.body.id) throw new Error('create session failed: ' + r.status);
  return r.body.id;
}
async function waitServer(ctx, sid, pred, ms = 60000) {
  const end = Date.now() + ms;
  while (Date.now() < end) {
    const st = (await api(ctx, 'GET', '/api/sessions/' + encodeURIComponent(sid) + '/status')).body;
    if (pred(st)) return st;
    await new Promise(r => setTimeout(r, 250));
  }
  throw new Error('server wait timeout');
}
async function layoutChecks(page, tag) {
  const m = await page.evaluate(() => {
    const vis = el => el && !el.hidden && el.getClientRects().length > 0;
    const r = el => { const b = el.getBoundingClientRect(); return { w: Math.round(b.width), h: Math.round(b.height), l: Math.round(b.left), r: Math.round(b.right), t: Math.round(b.top), b: Math.round(b.bottom) }; };
    const targets = [...document.querySelectorAll('#btnSend,#btnStop,#btnSendQueued,.chip-btn,#modelChip')].filter(vis).map(el => ({ id: el.id || el.className, ...r(el) }));
    return { sw: document.documentElement.scrollWidth, iw: window.innerWidth, ih: window.innerHeight, targets };
  });
  ok(tag + ': no horizontal scroll', m.sw <= m.iw, { sw: m.sw, iw: m.iw });
  const small = m.targets.filter(t => t.w < 44 || t.h < 44);
  ok(tag + ': tap targets >=44px', small.length === 0, small.length ? small : undefined);
  const out = m.targets.filter(t => t.l < 0 || t.r > m.iw || t.t < 0 || t.b > m.ih);
  ok(tag + ': controls inside viewport', out.length === 0, out.length ? out : undefined);
  return m;
}
async function shot(page, name) { if (SHOTS) await page.screenshot({ path: `${OUT}/${name}.png` }); }

async function flow(browser, vp, label) {
  const ctx = await browser.newContext({ viewport: vp, ignoreHTTPSErrors: true });
  await ctx.addInitScript(t => localStorage.setItem('grok_bridge_token', t), TOK);
  const sid = await newSess(ctx, 'Queue demo ' + label);
  const page = await ctx.newPage();
  await page.goto(BASE + '/#s=' + encodeURIComponent(sid));
  await page.waitForSelector('#input:not([disabled])');
  await page.waitForTimeout(600);
  await layoutChecks(page, label + ' idle');
  ok(label + ' idle: Stop hidden', await page.isHidden('#btnStop'));
  await shot(page, `${label}-1-idle`);

  await page.fill('#input', 'Explain the queue feature in one paragraph');
  await page.press('#input', 'Enter');
  await page.waitForSelector('#btnStop:visible', { timeout: 10000 });
  await page.waitForTimeout(700);
  ok(label + ' running: Send enabled', await page.isEnabled('#btnSend'));
  ok(label + ' running: Send says Queue', (await page.textContent('#btnSend')).trim() === 'Queue');
  await layoutChecks(page, label + ' running');
  await shot(page, `${label}-2-running`);

  await page.fill('#input', 'Then give me a short example');
  await page.press('#input', 'Enter');
  await page.fill('#input', 'And finally a one-line summary, please — this one is intentionally long to test truncation on narrow phones');
  await page.click('#btnSend');
  await page.waitForFunction(() => document.querySelectorAll('.queue-chip').length === 2, null, { timeout: 5000 });
  const chips = await page.$$eval('.queue-chip .chip-text', els => els.map(e => e.textContent));
  ok(label + ' queued: 2 chips in FIFO order', chips[0].startsWith('Then') && chips[1].startsWith('And finally'), chips);
  ok(label + ' queued: input cleared', (await page.inputValue('#input')) === '');
  const st = (await api(ctx, 'GET', '/api/sessions/' + encodeURIComponent(sid) + '/status')).body;
  ok(label + ' queued: server queue matches', st.queue.length === 2 && st.state === 'running');
  await layoutChecks(page, label + ' queued');
  await shot(page, `${label}-3-queued`);

  // Edit chip 2 before it is sent.
  await page.click('.queue-chip:nth-child(2) .chip-btn:has-text("Edit")');
  await page.fill('.queue-edit', 'And finally a one-line summary');
  await layoutChecks(page, label + ' editing');
  await shot(page, `${label}-3b-editing`);
  await page.press('.queue-edit', 'Enter');
  await page.waitForFunction(() => [...document.querySelectorAll('.queue-chip .chip-text')].some(e => e.textContent === 'And finally a one-line summary'), null, { timeout: 5000 });
  ok(label + ' edit persisted', true);

  // Wait for everything to drain (auto-send after each normal turn).
  await waitServer(ctx, sid, s => s.state === 'idle' && s.queue.length === 0, 120000);
  await page.waitForSelector('#btnStop', { state: 'hidden', timeout: 8000 });
  await page.waitForFunction(() => document.querySelectorAll('.msg.user').length >= 3 && !document.querySelector('.msg.streaming'), null, { timeout: 10000 });
  await page.waitForTimeout(800);
  const users = await page.$$eval('.msg.user .body', els => els.map(e => e.textContent.trim()));
  ok(label + ' after: 3 user turns in order', users.length === 3 && users[1].startsWith('Then') && users[2] === 'And finally a one-line summary', users);
  ok(label + ' after: tray hidden', await page.isHidden('#queueTray'));
  const asst = await page.$$eval('.msg.assistant .body', els => els.map(e => e.textContent.trim().slice(0, 40)));
  ok(label + ' after: 3 distinct replies, no duplicates', asst.length === 3 && new Set(asst).size === 3, asst);
  const dist = await page.evaluate(() => { const f = document.getElementById('feed'); return Math.round(f.scrollHeight - f.scrollTop - f.clientHeight); });
  ok(label + ' after: feed pinned to bottom', dist < 96, { dist });
  await layoutChecks(page, label + ' after');
  await shot(page, `${label}-4-after`);
  await ctx.close();
}

async function missedEvent(browser) {
  // Drop assistant_done and turn_status frames: UI must still clear Stop.
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, ignoreHTTPSErrors: true });
  await ctx.addInitScript(t => localStorage.setItem('grok_bridge_token', t), TOK);
  const sid = await newSess(ctx, 'Missed event');
  const page = await ctx.newPage();
  let dropped = 0;
  await page.routeWebSocket(/\/ws$/, ws => {
    const server = ws.connectToServer();
    server.onMessage(m => {
      try { const d = JSON.parse(m); if (d.type === 'assistant_done' || d.type === 'turn_status') { dropped++; return; } } catch (_) {}
      ws.send(m);
    });
    ws.onMessage(m => server.send(m));
  });
  await page.goto(BASE + '/#s=' + encodeURIComponent(sid));
  await page.waitForSelector('#input:not([disabled])');
  await page.waitForTimeout(600);
  await page.fill('#input', 'missed event check');
  await page.press('#input', 'Enter');
  await page.waitForSelector('#btnStop:visible', { timeout: 10000 });
  ok('missed: Send enabled while running', await page.isEnabled('#btnSend'));
  await waitServer(ctx, sid, s => s.state === 'idle', 60000);
  const t0 = Date.now();
  await page.waitForSelector('#btnStop', { state: 'hidden', timeout: 10000 }).catch(() => {});
  const hidden = await page.isHidden('#btnStop');
  ok('missed: Stop clears after missed assistant_done', hidden, { dropped, clearedAfterMs: Date.now() - t0 });
  await page.waitForFunction(() => document.querySelectorAll('.msg.assistant .body').length >= 1 && !document.querySelector('.msg.streaming'), null, { timeout: 10000 }).catch(() => {});
  ok('missed: reply rendered, no streaming bubble', await page.evaluate(() => !document.querySelector('.msg.streaming') && /DemoAgent|You said|re:|\S/.test((document.querySelector('.msg.assistant:last-of-type .body') || {}).textContent || '')));
  await ctx.close();
}

async function stopKeepsQueue(browser) {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, ignoreHTTPSErrors: true });
  await ctx.addInitScript(t => localStorage.setItem('grok_bridge_token', t), TOK);
  const sid = await newSess(ctx, 'Stop keeps queue');
  const page = await ctx.newPage();
  await page.goto(BASE + '/#s=' + encodeURIComponent(sid));
  await page.waitForSelector('#input:not([disabled])');
  await page.waitForTimeout(600);
  await page.fill('#input', 'long running');
  await page.press('#input', 'Enter');
  await page.waitForSelector('#btnStop:visible');
  await page.fill('#input', 'queued behind stop');
  await page.press('#input', 'Enter');
  await page.waitForSelector('.queue-chip');
  await page.click('#btnStop');
  await page.waitForSelector('#btnStop', { state: 'hidden', timeout: 10000 });
  await page.waitForSelector('#btnSendQueued:visible', { timeout: 5000 });
  ok('stop: queue kept + paused', (await page.$$('.queue-chip')).length === 1 && await page.isVisible('#queuePausedNote'));
  await layoutChecks(page, 'stop paused');
  await shot(page, '390-5-stopped-paused');
  await page.click('#btnSendQueued');
  await page.waitForSelector('#btnStop:visible', { timeout: 10000 });
  ok('stop: Send queued now starts it', (await page.$$('.queue-chip')).length === 0);
  await page.click('#btnStop');
  await page.waitForSelector('#btnStop', { state: 'hidden', timeout: 10000 });
  await ctx.close();
}

async function reloadKeepsQueue(browser) {
  const ctx = await browser.newContext({ viewport: { width: 414, height: 896 }, ignoreHTTPSErrors: true });
  await ctx.addInitScript(t => localStorage.setItem('grok_bridge_token', t), TOK);
  const sid = await newSess(ctx, 'Reload keeps queue');
  const page = await ctx.newPage();
  await page.goto(BASE + '/#s=' + encodeURIComponent(sid));
  await page.waitForSelector('#input:not([disabled])');
  await page.waitForTimeout(600);
  await page.fill('#input', 'turn one');
  await page.press('#input', 'Enter');
  await page.waitForSelector('#btnStop:visible');
  await page.fill('#input', 'survives refresh');
  await page.press('#input', 'Enter');
  await page.waitForSelector('.queue-chip');
  await page.reload();
  await page.waitForSelector('.queue-chip', { timeout: 10000 });
  ok('reload: chip + Stop restored from server', (await page.textContent('.queue-chip .chip-text')) === 'survives refresh' && await page.isVisible('#btnStop'));
  await page.click('.queue-chip .chip-remove');
  await page.waitForSelector('#queueTray', { state: 'hidden' });
  ok('reload: remove chip works', true);
  await page.click('#btnStop');
  await page.waitForSelector('#btnStop', { state: 'hidden', timeout: 10000 });
  await ctx.close();
}

(async () => {
  const browser = await chromium.launch();
  const only = (process.env.ONLY || 'flow390,flow1280,flow360,flow414,missed,stop,reload').split(',');
  try {
    if (only.includes('missed')) await missedEvent(browser);
    if (only.includes('stop')) await stopKeepsQueue(browser);
    if (only.includes('reload')) await reloadKeepsQueue(browser);
    if (only.includes('flow390')) await flow(browser, { width: 390, height: 844 }, '390');
    if (only.includes('flow1280')) await flow(browser, { width: 1280, height: 800 }, '1280');
    if (only.includes('flow360')) await flow(browser, { width: 360, height: 740 }, '360');
    if (only.includes('flow414')) await flow(browser, { width: 414, height: 896 }, '414');
  } catch (e) {
    results.push({ name: 'exception', pass: false, extra: String(e && e.message || e).slice(0, 400) });
  }
  await browser.close();
  for (const r of results) console.log((r.pass ? 'PASS ' : 'FAIL ') + r.name + (r.extra && !r.pass ? '  ' + JSON.stringify(r.extra) : (r.extra && r.name.startsWith('missed') ? '  ' + JSON.stringify(r.extra) : '')));
  const failed = results.filter(r => !r.pass).length;
  console.log(`\n${results.length - failed}/${results.length} passed`);
  process.exit(failed ? 1 : 0);
})();
