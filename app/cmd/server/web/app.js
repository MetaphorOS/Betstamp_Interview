// NFL odds board: one EventSource, snapshot then deltas, rendered per game.
"use strict";

const state = {
  seq: 0,
  books: [],            // [{name, displayName, priceKind}]
  games: new Map(),     // id -> game
  slots: new Map(),     // gameId|identity -> {family, outcome, entries, best}
  health: {},           // book -> health
  mode: "best",
  format: load("format", "american"), // american | decimal
  league: load("league", "all"),      // all | nfl | nba
  offset: null,         // serverTime - Date.now(), best estimate (max over samples)
  lastMsgAt: 0,
  opens: 0,
  connected: false,
  debug: false,
  lat: {},              // book -> {all|pregame|live: {e2e: [], b2s: [], s2y: []}}
  clock: { valid: false }, // server clock vs NTP {offsetMs, errMs, measuredAt, valid}
  bclock: null,         // browser clock vs server, from /api/ping {offset, err}
  bsamples: [],         // recent ping-based clock samples
  rttMs: [],            // browser <-> server round trips
};
function load(k, def) {
  try { return localStorage.getItem("odds." + k) || def; } catch { return def; }
}
function save(k, v) {
  try { localStorage.setItem("odds." + k, v); } catch {}
}
const shown = new Map(); // cell key -> {text, line, am} last rendered, for flash
const dirty = new Set();
let fullRender = true;

const SHORT = { draftkings: "DK", fanduel: "FD", polymarket: "PM" };
const short = (b) => SHORT[b] || b;
const displayName = (b) => (state.books.find((x) => x.name === b) || {}).displayName || b;
// Server time now. The ping-based estimate (error ±half the round trip) is
// used once it exists; until then only the coarse max-sample estimate, which
// is good enough for "3s ago" labels but never feeds latency figures.
const serverNow = () => Date.now() + (state.bclock ? state.bclock.offset : state.offset || 0);

// ---- time & formatting ----------------------------------------------------
function age(ms) {
  if (!ms) return "never";
  const s = Math.max(0, (serverNow() - ms) / 1000);
  if (s < 1) return "now";
  if (s < 60) return `${Math.floor(s)}s ago`;
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  return `${Math.floor(s / 3600)}h ago`;
}
function since(ms) {
  if (!ms) return "";
  const s = Math.max(0, (serverNow() - ms) / 1000);
  if (s < 60) return `${Math.floor(s)}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${Math.floor(s % 60)}s`;
  return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
}
const am = (v) => (v > 0 ? `+${v}` : `${v}`);
// price renders in the chosen format; decimal uses the precise value when the
// book sends one (DK/FD true odds) and converts otherwise.
function price(american, decimal) {
  if (state.format === "decimal") return (decimal > 1 ? decimal : dec(american)).toFixed(2);
  return am(american);
}
function lineText(family, outcome, line) {
  if (line == null) return "";
  if (family === "total") return `${outcome === "over" ? "O" : "U"} ${line}`;
  return line > 0 ? `+${line}` : `${line}`;
}
function esc(s) {
  return String(s).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" })[c]);
}
function quantile(arr, q) {
  if (!arr.length) return null;
  const s = [...arr].sort((a, b) => a - b);
  return s[Math.min(s.length - 1, Math.floor(q * s.length))];
}

// Better-for-the-bettor comparison, same rule as the server's best price.
function lineScore(family, outcome, line) {
  if (line == null) return 0;
  if (family === "spread") return line;
  if (family === "total") return outcome === "over" ? -line : line;
  return 0;
}
const dec = (a) => (a >= 100 ? 1 + a / 100 : a <= -100 ? 1 + 100 / -a : 0);
function direction(family, outcome, prev, cur) {
  const dl = lineScore(family, outcome, cur.line) - lineScore(family, outcome, prev.line);
  if (Math.abs(dl) > 1e-9) return dl > 0 ? "up" : "down";
  const dp = dec(cur.am) - dec(prev.am);
  if (Math.abs(dp) > 1e-9) return dp > 0 ? "up" : "down";
  return null;
}

// ---- identity -------------------------------------------------------------
// family|period|subject|stat|comparator|class|outcome
function mainId(family, outcome) {
  const stat = family === "total" ? "points" : "";
  const comp = family === "total" ? "ou" : "";
  return [family, "full", "event", stat, comp, "main", outcome].join("|");
}

// ---- stream ---------------------------------------------------------------
let es = null;
function connect() {
  const url = state.seq ? `/stream?since=${state.seq}` : "/stream";
  es = new EventSource(url);
  es.onopen = () => {
    state.opens++;
    state.connected = true;
    state.lastMsgAt = Date.now();
    renderConn();
  };
  es.onerror = () => {
    state.connected = false;
    renderConn(); // EventSource retries by itself, with Last-Event-ID
  };
  es.addEventListener("snapshot", (e) => onSnapshot(JSON.parse(e.data)));
  es.addEventListener("delta", (e) => onDelta(JSON.parse(e.data)));
  es.addEventListener("ping", (e) => {
    const p = JSON.parse(e.data);
    sampleClock(p.serverTime);
    if (p.clock) state.clock = p.clock;
    state.lastMsgAt = Date.now();
  });
}
// If nothing arrives for 35 s (pings come every 15 s) the connection is dead
// even if the browser has not noticed: reconnect and resume from our seq.
setInterval(() => {
  if (es && Date.now() - state.lastMsgAt > 35000) {
    es.close();
    state.connected = false;
    connect();
  }
}, 5000);

function sampleClock(serverTime) {
  if (!serverTime) return;
  // serverTime is never later than the real server clock when it arrives, so
  // the largest sample is the best estimate of the offset.
  const o = serverTime - Date.now();
  if (state.offset == null || o > state.offset) state.offset = o;
}

function onSnapshot(s) {
  state.lastMsgAt = Date.now();
  state.offset = null;
  sampleClock(s.serverTime);
  state.seq = s.seq;
  state.books = s.books || [];
  state.games = new Map((s.games || []).map((g) => [g.id, g]));
  state.slots = new Map();
  for (const sl of s.slots || []) {
    state.slots.set(`${sl.g}|${sl.id}`, { family: sl.family, outcome: sl.outcome, entries: sl.entries || {}, best: sl.best || null });
  }
  state.health = s.health || {};
  state.clock = s.clock || { valid: false };
  renderModes();
  fullRender = true;
  schedule();
}

function onDelta(d) {
  state.lastMsgAt = Date.now();
  state.seq = d.seq;
  sampleClock(d.now);
  if (d.k === "health") {
    const wasExcluded = bookExcluded(d.b);
    state.health[d.b] = d.h;
    renderHealth();
    // Re-render cells only when this book became (or stopped being) stale.
    if (wasExcluded !== bookExcluded(d.b)) {
      fullRender = true;
      schedule();
    }
    return;
  }
  if (d.k === "confirm") {
    // Unchanged prices re-confirmed by a stream frame, poll or snapshot:
    // freshness only, so no flash.
    for (const [key, at] of Object.entries(d.c || {})) {
      const e = state.slots.get(key)?.entries[d.b];
      if (e && at > (e.at || 0)) {
        e.at = at;
        dirty.add(gameOf(key));
      }
    }
    schedule();
    return;
  }
  if (d.k === "game") {
    if (d.rm) state.games.delete(d.g);
    else state.games.set(d.g, d.game);
    renderModes();
    fullRender = true;
    schedule();
    return;
  }
  // slot
  const key = `${d.g}|${d.id}`;
  let sl = state.slots.get(key);
  if (!sl) {
    const parts = d.id.split("|");
    sl = { family: parts[0], outcome: parts[6], entries: {}, best: null };
    state.slots.set(key, sl);
  }
  if (d.b) {
    if (d.rm) delete sl.entries[d.b];
    else if (d.e) sl.entries[d.b] = d.e;
  }
  sl.best = d.best || null;
  if (d.src === "stream" && d.b && d.recv && d.e && state.bclock) {
    // Hops, all in server time. Nothing is recorded until the browser clock
    // is estimated from pings; book clocks are compared only against a valid
    // NTP-corrected server clock, so uncorrected samples never mix in.
    const now = serverNow();
    const s2y = now - d.recv;
    const ok = d.bts && state.clock.valid;
    const b2s = ok ? d.recv + state.clock.offsetMs - d.bts : null;
    const e2e = ok ? now + state.clock.offsetMs - d.bts : null;
    const byBook = (state.lat[d.b] ||= {});
    for (const phase of ["all", d.e.live ? "live" : "pregame"]) {
      const l = (byBook[phase] ||= { e2e: [], b2s: [], s2y: [] });
      push(l.s2y, s2y);
      if (b2s != null) push(l.b2s, b2s);
      if (e2e != null) push(l.e2e, e2e);
    }
  }
  dirty.add(d.g);
  schedule();
}
// A slot key is gameId|identity; the game id has "|" in it and the identity
// has exactly 7 fields.
const gameOf = (key) => key.split("|").slice(0, -7).join("|");
function push(arr, v) {
  arr.push(v);
  if (arr.length > 300) arr.shift();
}

let raf = 0;
function schedule() {
  if (!raf) raf = requestAnimationFrame(flush);
}
function flush() {
  raf = 0;
  if (fullRender) {
    fullRender = false;
    dirty.clear();
    renderBoard();
  } else {
    for (const g of dirty) renderGame(g);
    dirty.clear();
  }
  renderConn();
}

// ---- modes ----------------------------------------------------------------
function renderModes() {
  const el = document.getElementById("modes");
  const opts = [{ name: "best", label: "Best Price" }].concat(
    state.books.map((b) => ({ name: b.name, label: b.displayName }))
  );
  el.innerHTML = opts
    .map((o) => `<button type="button" data-mode="${esc(o.name)}" aria-pressed="${o.name === state.mode}">${esc(o.label)}</button>`)
    .join("");
  const seg = (id, key, items) => {
    document.getElementById(id).innerHTML = items
      .map(([v, label]) => `<button type="button" data-${key}="${v}" aria-pressed="${state[key] === v}">${label}</button>`)
      .join("");
  };
  const sports = [...new Set([...state.games.values()].map((g) => g.sport))].sort();
  seg("leagues", "league", [["all", "All"], ...sports.map((sp) => [sp, sp.toUpperCase()])]);
  seg("formats", "format", [["american", "American"], ["decimal", "Decimal"]]);
}
for (const [id, key] of [["leagues", "league"], ["formats", "format"]]) {
  document.getElementById(id).addEventListener("click", (e) => {
    const b = e.target.closest(`button[data-${key}]`);
    if (!b) return;
    state[key] = b.dataset[key];
    save(key, state[key]);
    shown.clear(); // a display change is not a price move: no flash
    renderModes();
    fullRender = true;
    schedule();
  });
}
document.getElementById("modes").addEventListener("click", (e) => {
  const b = e.target.closest("button[data-mode]");
  if (!b) return;
  state.mode = b.dataset.mode;
  shown.clear(); // switching source is not a price move: no flash
  renderModes();
  fullRender = true;
  schedule();
});

// ---- board ----------------------------------------------------------------
function isLive(g) {
  if (g.live) return true;
  for (const [key, sl] of state.slots) {
    if (key.startsWith(g.id + "|") && Object.values(sl.entries).some((e) => e.live)) return true;
  }
  return false;
}
function hasPrices(g) {
  for (const key of state.slots.keys()) if (key.startsWith(g.id + "|")) return true;
  return false;
}

function renderBoard() {
  const main = document.getElementById("board");
  const games = [...state.games.values()].filter((g) => hasPrices(g) && (state.league === "all" || g.sport === state.league));
  if (!games.length) {
    main.innerHTML = `<p class="empty">No games on the board yet.</p>`;
    return;
  }
  games.sort((a, b) => a.start - b.start || a.id.localeCompare(b.id));
  // Status comes from the server: live, suspended (started, nothing open for
  // under 3 min), closed (nothing open for 3 min: likely final), pregame.
  const st = (g) => g.status || (isLive(g) ? "live" : "pregame");
  const live = games.filter((g) => st(g) === "live" || st(g) === "suspended");
  const closed = games.filter((g) => st(g) === "closed");
  const pre = games.filter((g) => st(g) === "pregame");
  const byDay = new Map();
  for (const g of pre) {
    const day = new Date(g.start).toLocaleDateString(undefined, { weekday: "long", month: "short", day: "numeric" });
    if (!byDay.has(day)) byDay.set(day, []);
    byDay.get(day).push(g);
  }
  let html = "";
  if (live.length) html += section(`<span class="badge-live">LIVE</span> In play`, live);
  for (const [day, gs] of byDay) html += section(`Pregame · ${esc(day)}`, gs);
  if (closed.length) html += section(`Closed · likely final <span class="hint">no book has an open price; removed after 15 min</span>`, closed);
  main.innerHTML = html;
  for (const g of games) renderGame(g.id);
}
function section(title, games) {
  return `<h2 class="section-title">${title} <span class="count">${games.length}</span></h2>
    <div class="games">${games.map((g) => `<article class="game" id="game-${cssId(g.id)}"></article>`).join("")}</div>`;
}
const cssId = (s) => s.replace(/[^a-zA-Z0-9_-]/g, "_");

function renderGame(gid) {
  const g = state.games.get(gid);
  const el = document.getElementById(`game-${cssId(gid)}`);
  if (!g || !el) {
    if (g && !el && hasPrices(g)) fullRender = true, schedule(); // a new game appeared
    return;
  }
  const status = g.status || (isLive(g) ? "live" : "pregame");
  const when = {
    live: `<span class="badge-live">LIVE</span>`,
    suspended: `<span class="badge-susp" title="In play, every market suspended right now">SUSPENDED</span>`,
    closed: `<span class="badge-closed" title="No book has offered an open price for 3 minutes">CLOSED</span>`,
  }[status] || new Date(g.start).toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" });
  el.classList.toggle("is-closed", status === "closed");
  const team = (t) => {
    const name = t.name || t.id;
    const i = name.lastIndexOf(" ");
    return i > 0 ? `${esc(name.slice(i + 1))}<small>${esc(name.slice(0, i))}</small>` : esc(name);
  };
  const row = (t, side) => `<tr>
      <td class="team">${team(t)}</td>
      <td>${cell(gid, "spread", t.id)}</td>
      <td>${cell(gid, "total", side === "away" ? "over" : "under")}</td>
      <td>${cell(gid, "moneyline", t.id)}</td>
    </tr>`;
  const sport = state.league === "all" ? `<span class="sport">${esc((g.sport || "").toUpperCase())}</span>` : "";
  el.innerHTML = `<div class="ghead"><span>${sport}${esc(g.away.name)} @ ${esc(g.home.name)}</span>${when}</div>
    <table>
      <thead><tr><th class="team-h"></th><th>Spread</th><th>Total</th><th>Moneyline</th></tr></thead>
      <tbody>${row(g.away, "away")}${row(g.home, "home")}</tbody>
    </table>`;
}

function bookExcluded(b) {
  const h = state.health[b];
  return !h || h.state === "stale" || h.state === "down";
}

function cell(gid, family, outcome) {
  const key = `${gid}|${mainId(family, outcome)}`;
  const sl = state.slots.get(key);
  let book = null, e = null;
  if (sl) {
    if (state.mode === "best") {
      if (sl.best) {
        book = sl.best.book;
        e = sl.entries[book] || { line: sl.best.line, am: sl.best.american, st: "open" };
      }
    } else if (sl.entries[state.mode]) {
      book = state.mode;
      e = sl.entries[book];
    }
  }
  if (!e) {
    // In best mode, say why nothing is shown when some book does quote it.
    const why = state.mode === "best" && sl && Object.keys(sl.entries).length ? "no eligible price" : "&nbsp;";
    shown.delete(`${key}|${state.mode}`);
    return `<div class="cell none"><span class="px">—</span><span class="meta">${why}</span></div>`;
  }
  const ln = lineText(family, outcome, e.line);
  const text = `${ln}|${e.am}`;
  const skey = `${key}|${state.mode}`;
  const prevShown = shown.get(skey);
  let cls = "cell";
  let arrow = "";
  if (prevShown && prevShown.text !== text) {
    const dir = direction(family, outcome, prevShown, { line: e.line, am: e.am });
    if (dir) {
      cls += dir === "up" ? " flash-up" : " flash-down";
      arrow = dir === "up" ? `<span class="arrow-up">▲</span>` : `<span class="arrow-down">▼</span>`;
    }
  }
  shown.set(skey, { text, line: e.line, am: e.am });
  if (e.st === "suspended") cls += " susp";
  if (bookExcluded(book)) cls += " stale";
  if (isOld(e.at)) cls += " old";
  const recent = e.prev && e.at && serverNow() - e.at < 60000;
  const prev = recent ? `<span class="prev">${esc(lineText(family, outcome, e.prev.line))} ${price(e.prev.american, 0)}</span>` : "";
  const who = state.mode === "best" ? `${esc(short(book))} · ` : "";
  const title = `${displayName(book)} · updated ${age(e.at)}${e.live ? " · in play" : ""}`;
  return `<div class="${cls}" title="${esc(title)}">
      <span class="old-icon" aria-label="Price not confirmed for over 5 minutes" title="Price not confirmed for over 5 minutes">⚠</span>
      <span class="px">${ln ? `<span class="ln">${esc(ln)}</span>` : ""}${price(e.am, e.dec)} ${arrow}</span>
      ${prev}
      <span class="meta" data-at="${e.at || 0}" data-who="${who}">${who}${age(e.at)}</span>
    </div>`;
}

// A price is old when we have not confirmed it (stream update, or a poll or
// snapshot seeing the same price) for 5 minutes. Polls run every 30-60 s.
const OLD_AFTER_MS = 5 * 60 * 1000;
const isOld = (at) => !!at && serverNow() - at > OLD_AFTER_MS;

// Ages tick without re-rendering cells (which would re-trigger flashes).
setInterval(() => {
  for (const m of document.querySelectorAll(".cell .meta[data-at]")) {
    m.textContent = m.dataset.who + age(Number(m.dataset.at));
    m.closest(".cell").classList.toggle("old", isOld(Number(m.dataset.at)));
  }
  renderHealth();
  renderConn();
}, 1000);

// ---- health ---------------------------------------------------------------
function renderHealth() {
  const el = document.getElementById("health");
  if (!state.books.length) return;
  el.innerHTML = state.books.map((b) => healthCard(b)).join("");
}
function healthCard(b) {
  const h = state.health[b.name] || { state: "starting" };
  const st = h.state || "starting";
  const wsUp = !!h.connectedSince;
  const beatAge = h.lastBeatAt ? (serverNow() - h.lastBeatAt) / 1000 : null;
  const beatCls = beatAge == null ? "" : beatAge < 35 ? "ok" : beatAge < 90 ? "warn" : "bad";
  const pollAge = h.lastPollAt ? (serverNow() - h.lastPollAt) / 1000 : null;
  const lat = state.lat[b.name] || {};
  // Book -> screen needs the book's publish time; FanDuel frames have none.
  const noBookTime = b.name === "fanduel";
  const ms = (v) => `${Math.round(v)} ms`;
  // Error bounds: server clock ±half its NTP round trip; browser clock
  // ±half its ping round trip. book→screen carries both.
  const pm = (v) => `±${Math.max(1, Math.round(v))}`;
  const srvErr = state.clock.valid ? state.clock.errMs : null;
  const brErr = state.bclock ? state.bclock.err : null;
  const e2eText = (phase) => {
    if (noBookTime) return `<span class="muted">—</span>`;
    if (!state.bclock) return `<span class="muted">syncing browser clock…</span>`;
    const l = lat[phase];
    if (!l || !l.e2e.length) {
      if (!state.clock.valid) return `<span class="warn">uncorrected: server clock unknown</span>`;
      return `<span class="muted">waiting for ${phase === "all" ? "" : phase + " "}updates</span>`;
    }
    return `${ms(quantile(l.e2e, 0.5))} <span class="muted">${srvErr != null ? pm(srvErr + brErr) : "±?"} · p95 ${ms(quantile(l.e2e, 0.95))} · n=${l.e2e.length}</span>`;
  };
  const all = lat.all;
  const hops = all
    ? [
        all.b2s.length ? `book→server ${ms(quantile(all.b2s, 0.5))} ${srvErr != null ? pm(srvErr) : ""}` : null,
        `server→you ${ms(quantile(all.s2y, 0.5))} ${brErr != null ? pm(brErr) : ""}`,
      ].filter(Boolean).join(" · ")
    : "waiting for stream updates";
  // Handshake: the same measure for every book, an app-level request and its
  // reply at connect. Ping: each book's own keep-alive round trip.
  const HS = {
    draftkings: "subscribe → subscribed",
    fanduel: "connection_init → ack",
    polymarket: "PING → PONG once subscribed",
  };
  const hs = h.handshakeMs
    ? `${ms(h.handshakeMs)} <span class="muted">${esc(HS[b.name] || "request → reply")} · ${age(h.handshakeAt)}</span>`
    : `<span class="muted">waiting for a connect</span>`;
  const PING = {
    draftkings: "socket ping→pong",
    fanduel: "socket ping→pong; FanDuel holds pongs ~1 s",
    polymarket: "PING→PONG message",
  };
  const ping = h.pingMs
    ? `${ms(h.pingMs)} <span class="muted">p50 ${ms(h.pingP50Ms)} · ${esc(PING[b.name] || "ping")}</span>`
    : `<span class="muted">waiting for a pong</span>`;
  const debug = state.debug
    ? `<div class="hbtns">
         <button type="button" data-kill="${esc(b.name)}" title="Close the book's socket; the supervisor reconnects">Drop socket</button>
         <button type="button" data-pause="${esc(b.name)}" title="Stream and polls both down for 45 s">Take offline 45s</button>
       </div>`
    : "";
  return `<div class="hcard is-${st}">
    <div class="hhead"><b>${esc(b.displayName)}</b><span class="pill ${st}">${st}</span></div>
    <div class="hrow"><span>WebSocket</span><span class="${wsUp ? "ok" : "bad"}">${wsUp ? `connected ${since(h.connectedSince)}` : "not connected"}${
      h.lastBeatAt ? ` · <span class="${beatCls}">heartbeat ${age(h.lastBeatAt)}</span>` : ""}</span></div>
    <div class="hrow"><span>Polling</span><span>${h.polls ? `<span class="${pollAge != null && pollAge < 60 ? "ok" : "warn"}">last ok ${age(h.lastPollAt)}</span> · ` : ""}${h.polls || 0} ok · <span class="${h.pollErrors ? "bad" : ""}">${h.pollErrors || 0} failed</span></span></div>
    <div class="hrow"><span>Snapshots</span><span>${h.snapshots || 0} ok · <span class="${h.snapshotErrors ? "bad" : ""}">${h.snapshotErrors || 0} failed</span>${h.lastSnapshotAt ? ` · last ${age(h.lastSnapshotAt)}` : ""}</span></div>
    <div class="hrow"><span>Recovery</span><span><span class="${h.reconnects ? "warn" : ""}">${h.reconnects || 0} reconnects</span> · <span class="${h.resyncs ? "warn" : ""}">${h.resyncs || 0} resyncs</span></span></div>
    <div class="hrow"><span>Data</span><span>last ${age(h.lastDataAt)} · ${h.updates || 0} updates</span></div>
    <div class="hrow" title="Book publish time to this page handling the update, p50 over recent stream updates (clock-corrected)"><span>Latency</span><span>${e2eText("all")}</span></div>
    <div class="hrow sub"><span>pregame</span><span>${e2eText("pregame")}</span></div>
    <div class="hrow sub"><span>live</span><span>${e2eText("live")}</span></div>
    <div class="hrow sub"><span>hops</span><span class="muted">${hops}</span></div>
    <div class="hrow" title="App-level request and its reply when the socket connects: the same measure for every book"><span>Handshake</span><span>${hs}</span></div>
    <div class="hrow" title="Each book's keep-alive round trip, repeated while connected"><span>Ping</span><span>${ping}</span></div>
    ${h.lastError ? `<div class="herr" title="last error">last error: ${esc(h.lastError)}</div>` : ""}
    ${h.lastPollError ? `<div class="herr">poll error: ${esc(h.lastPollError)}</div>` : ""}
    ${debug}
  </div>`;
}
document.getElementById("health").addEventListener("click", async (e) => {
  const k = e.target.closest("[data-kill]");
  const p = e.target.closest("[data-pause]");
  if (k) await fetch(`/debug/kill/${encodeURIComponent(k.dataset.kill)}`, { method: "POST" });
  if (p) await fetch(`/debug/pause/${encodeURIComponent(p.dataset.pause)}?for=45s`, { method: "POST" });
});

// ---- browser connection ---------------------------------------------------
function renderConn() {
  const el = document.getElementById("conn");
  const quiet = state.lastMsgAt ? (Date.now() - state.lastMsgAt) / 1000 : null;
  let cls = "conn", text;
  if (!state.connected) {
    cls += " bad";
    text = state.opens ? "disconnected · reconnecting…" : "connecting…";
  } else if (quiet > 20) {
    cls += " warn";
    text = `quiet ${Math.floor(quiet)}s…`;
  } else {
    cls += " ok";
    text = `live · seq ${state.seq}`;
  }
  const reconnects = Math.max(0, state.opens - 1);
  text += ` · ${reconnects} reconnect${reconnects === 1 ? "" : "s"}`;
  if (state.rttMs.length) text += ` · ping ${Math.round(quantile(state.rttMs, 0.5))} ms`;
  if (state.bclock) text += ` · clock sync ±${state.bclock.err.toFixed(1)} ms`;
  text += state.clock.valid ? ` · server NTP ±${state.clock.errMs.toFixed(1)} ms` : " · server NTP unknown";
  el.className = cls;
  el.querySelector(".conn-text").textContent = text;
}

// Browser <-> server round trip, and the browser clock estimate (Cristian):
// the server stamped serverTime somewhere inside the round trip, so assume
// the midpoint; the error is at most half the round trip (+0.5 ms for the
// millisecond timestamp). The lowest-round-trip recent sample wins.
async function pingServer() {
  const t0 = performance.now();
  try {
    const r = await fetch("/api/ping", { cache: "no-store" });
    const { serverTime } = await r.json();
    const rtt = performance.now() - t0;
    const mid = Date.now() - rtt / 2;
    state.rttMs.push(rtt);
    if (state.rttMs.length > 30) state.rttMs.shift();
    state.bsamples.push({ offset: serverTime - mid, err: rtt / 2 + 0.5 });
    if (state.bsamples.length > 30) state.bsamples.shift();
    state.bclock = state.bsamples.reduce((a, b) => (b.err < a.err ? b : a));
    renderConn();
  } catch {}
}
// A quick burst so the estimate is usable within a couple of seconds, then
// every 10 s (30 samples: the last 5 minutes).
for (let i = 0; i < 5; i++) setTimeout(pingServer, i * 300);
setInterval(pingServer, 10000);

fetch("/debug").then((r) => r.json()).then((d) => { state.debug = !!d.enabled; renderHealth(); }).catch(() => {});
connect();
