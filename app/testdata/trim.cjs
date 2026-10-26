// Regenerates the trimmed soak replay fixtures in this directory from
// out/soak-1h (which is gitignored). Every received frame is kept; only fields
// the app never parses are dropped. Run from the repo root:
//
//   node app/testdata/trim.cjs
//
// Output lines: {"at","conn","payload":<object>} (DK) and the same for FD.
const fs = require("fs");
const path = require("path");

const root = path.resolve(__dirname, "..", "..");
const soak = path.join(root, "out", "soak-1h");

function pick(o, keys) {
  if (!o || typeof o !== "object") return o;
  const out = {};
  for (const k of keys) if (o[k] !== undefined) out[k] = o[k];
  return out;
}

function trimEvent(e) {
  const out = pick(e, ["id", "name", "startEventDate", "status"]);
  if (Array.isArray(e.participants)) out.participants = e.participants.map((p) => pick(p, ["name", "venueRole"]));
  return out;
}
function trimMarket(m) {
  const out = pick(m, ["id", "eventId", "name", "isSuspended"]);
  if (m.marketType) out.marketType = pick(m.marketType, ["name"]);
  if (m.marketIdentifier) out.marketIdentifier = pick(m.marketIdentifier, ["isLive"]);
  return out;
}
function trimSelection(s) {
  const out = pick(s, ["id", "marketId", "label", "outcomeType", "points", "trueOdds", "status", "tags", "replacedSelectionId"]);
  if (s.displayOdds) out.displayOdds = pick(s.displayOdds, ["american", "decimal"]);
  return out;
}
const trimmers = { events: trimEvent, markets: trimMarket, selections: trimSelection };

function trimDK(p) {
  if (p.event !== "update") return p;
  const d = p.data && p.data.data;
  const out = { id: p.id, event: p.event, websocketPublishTimestamp: p.websocketPublishTimestamp, data: { data: {}, metadata: p.data.metadata } };
  for (const op of ["add", "change", "remove"]) {
    if (!d || !d[op]) continue;
    const section = {};
    for (const ent of ["events", "markets", "selections"]) {
      const v = d[op][ent];
      if (!Array.isArray(v) || v.length === 0) continue;
      section[ent] = op === "remove" ? v : v.map(trimmers[ent]);
    }
    out.data.data[op] = section;
  }
  return out;
}

function convert(src, dst, trim) {
  const lines = fs.readFileSync(src, "utf8").trim().split("\n");
  const out = [];
  for (const l of lines) {
    const o = JSON.parse(l);
    if (o.kind !== "received") continue;
    const p = JSON.parse(o.payload);
    out.push(JSON.stringify({ at: o.at, conn: o.conn, payload: trim(p) }));
  }
  fs.writeFileSync(dst, out.join("\n") + "\n");
  console.log(dst, out.length, "frames");
}

convert(path.join(soak, "dk", "ws.jsonl"), path.join(__dirname, "soak-dk-ws.jsonl"), trimDK);
convert(path.join(soak, "fd", "ws.jsonl"), path.join(__dirname, "soak-fd-ws.jsonl"), (p) => p);

// FD discovery boards: each FD connection in the soak started from one. Keep
// only the three main market types (what the app subscribes to) and the
// events they reference.
const fdMain = new Set(["MONEY_LINE", "MATCH_HANDICAP_(2-WAY)", "TOTAL_POINTS_(OVER/UNDER)"]);
const fdRest = path.join(soak, "fd", "rest");
for (const f of fs.readdirSync(fdRest).filter((f) => /^bootstrap-\d+-[^.]+\.json$/.test(f)).sort()) {
  const board = JSON.parse(fs.readFileSync(path.join(fdRest, f), "utf8"));
  const markets = {};
  const events = {};
  for (const [id, m] of Object.entries(board.attachments.markets || {})) {
    if (!fdMain.has(m.marketType)) continue;
    markets[id] = {
      ...pick(m, ["marketId", "eventId", "marketName", "marketType", "marketStatus", "inPlay"]),
      runners: (m.runners || []).map((r) => pick(r, ["selectionId", "handicap", "runnerName", "runnerStatus", "result", "winRunnerOdds"])),
    };
    const e = board.attachments.events[String(m.eventId)];
    if (e) events[String(m.eventId)] = pick(e, ["eventId", "name", "openDate"]);
  }
  const conn = Number(f.split("-")[1]);
  const dst = path.join(__dirname, `soak-fd-bootstrap-${conn}.json`);
  fs.writeFileSync(dst, JSON.stringify({ attachments: { events, markets } }));
  console.log(dst, Object.keys(markets).length, "markets");
}
fs.copyFileSync(path.join(fdRest, "resync-c1-b1-0001-20261025T162028Z.json"), path.join(__dirname, "soak-fd-smp.json"));
