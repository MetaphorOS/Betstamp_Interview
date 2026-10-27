// Regenerates the NFL player-prop fixtures from the 2026-10-27 research
// capture (research/out/props-2026-10-27, gitignored). One game, Panthers @
// Packers, at all three books; only fields the app parses are kept. Run from
// the repo root:
//
//   node app/testdata/trim-props.cjs
const fs = require("fs");
const path = require("path");

const root = path.resolve(__dirname, "..", "..");
const src = path.join(root, "research", "out", "props-2026-10-27");
const J = (...p) => JSON.parse(fs.readFileSync(path.join(src, ...p), "utf8"));
const out = (name, v) => fs.writeFileSync(path.join(__dirname, name), JSON.stringify(v));
const pick = (o, keys) => {
  if (!o || typeof o !== "object") return o;
  const r = {};
  for (const k of keys) if (o[k] !== undefined) r[k] = o[k];
  return r;
};

// ---- DraftKings: league body + every prop subcategory, one event ----
const dkMain = J("dk-nfl-wide", "main.json");
const ev = dkMain.events.find((e) => /Packers/.test(e.name) && /Panthers/.test(e.name));
const keep = (d) => {
  const markets = d.markets.filter((m) => String(m.eventId) === String(ev.id));
  const ids = new Set(markets.map((m) => String(m.id)));
  return {
    events: d.events.filter((e) => String(e.id) === String(ev.id)).map((e) => ({
      ...pick(e, ["id", "name", "startEventDate", "status", "leagueId"]),
      participants: (e.participants || []).map((p) => pick(p, ["name", "venueRole"])),
    })),
    markets: markets.map((m) => ({ ...pick(m, ["id", "eventId", "name", "isSuspended"]), marketType: pick(m.marketType, ["name"]) })),
    selections: d.selections.filter((s) => ids.has(String(s.marketId))).map((s) => ({
      ...pick(s, ["id", "marketId", "label", "outcomeType", "points", "trueOdds", "status", "milestoneValue"]),
      displayOdds: pick(s.displayOdds, ["american"]),
      participants: s.participants ? s.participants.map((p) => pick(p, ["name", "type"])) : undefined,
    })),
    subscriptionPartials: d.subscriptionPartials,
  };
};
const dk = { main: keep(dkMain), props: [] };
for (const f of fs.readdirSync(path.join(src, "dk-nfl-wide")).filter((f) => /^prop-\d+\.json$/.test(f)).sort()) {
  dk.props.push(keep(J("dk-nfl-wide", f)));
}
out("dk-nfl-props.json", dk);

// ---- FanDuel: the event's MGA inventory + its SMP prices ----
let fdEvent;
for (const f of fs.readdirSync(path.join(src, "fd-nfl")).filter((f) => /-mga\.json$/.test(f))) {
  const a = J("fd-nfl", f).attachments;
  const e = Object.values(a.events || {})[0];
  if (e && /Packers/.test(e.name) && /Panthers/.test(e.name)) fdEvent = a;
}
const fdMarkets = {};
for (const [k, m] of Object.entries(fdEvent.sportsBookMarkets)) {
  fdMarkets[k] = {
    ...pick(m, ["marketId", "eventId", "marketName", "marketType", "marketStatus", "inPlay"]),
    runners: m.runners.map((r) => ({ ...pick(r, ["selectionId", "handicap", "runnerName", "runnerStatus"]), result: r.result })),
  };
}
const ids = new Set(Object.values(fdMarkets).map((m) => m.marketId));
const prices = J("fd-nfl", "prices.json").filter((p) => ids.has(p.marketId)).map((p) => ({
  ...pick(p, ["marketId", "marketStatus", "inplay"]),
  runnerDetails: p.runnerDetails.map((r) => ({
    ...pick(r, ["selectionId", "handicap", "runnerStatus"]),
    winRunnerOdds: {
      americanDisplayOdds: pick(r.winRunnerOdds?.americanDisplayOdds, ["americanOddsInt"]),
      trueOdds: r.winRunnerOdds?.trueOdds,
    },
  })),
}));
out("fd-nfl-mga.json", { attachments: { events: fdEvent.events, sportsBookMarkets: fdMarkets } });
out("fd-nfl-smp.json", prices);

// ---- BetMGM: fixture-view with every market ----
const fx = J("mgm-nfl", "view-6_48482.json").fixture;
const trimMGM = (m) => ({
  ...pick(m, ["id", "name", "status", "attr", "spread", "isMain", "fixtureParticipantId", "source"]),
  parameters: m.parameters,
  options: m.options.map((o) => ({
    ...pick(o, ["id", "status", "name", "attr", "totalsPrefix", "participantId"]),
    price: pick(o.price, ["odds", "americanOdds"]),
    parameters: o.parameters,
  })),
});
out("mgm-nfl-view.json", {
  fixture: {
    ...pick(fx, ["id", "name", "source", "context", "stage", "startDate", "isOpenForBetting", "fixtureType"]),
    participants: fx.participants.map((p) => pick(p, ["id", "participantId", "name", "properties"])),
    optionMarkets: fx.optionMarkets.map(trimMGM),
  },
});

// ---- BetMGM NBA listing and a push sample (research/out/mgm-nba) ----
const nba = JSON.parse(fs.readFileSync(path.join(root, "research", "out", "mgm-nba", "fixtures-nba.json"), "utf8"));
out("mgm-nba-fixtures.json", {
  totalCount: nba.totalCount,
  fixtures: nba.fixtures.map((f) => ({
    ...pick(f, ["id", "name", "source", "context", "stage", "startDate", "isOpenForBetting", "fixtureType"]),
    participants: f.participants.map((p) => pick(p, ["id", "participantId", "name", "properties"])),
    optionMarkets: f.optionMarkets.map(trimMGM),
  })),
});
// Push frames for the fixtures above, as received (one SignalR message per
// line, without the 0x1e terminator).
const ws = fs.readFileSync(path.join(root, "research", "out", "mgm-nba", "ws.log"), "utf8").split("\n")
  .map((l) => l.split("\t")[1]).filter((m) => m && (/20030815|6:48482|MainToLive/.test(m) || m === "{}" || /"type":[36]/.test(m)));
fs.writeFileSync(path.join(__dirname, "mgm-ws.jsonl"), ws.join("\n") + "\n");
