// Trims out/pm-nfl-events.json (Gamma NFL events, gitignored) to the game
// events and main market types the polymarket book reads. Run from repo root:
//   node app/testdata/trim-pm.cjs
const fs = require("fs");
const path = require("path");
const root = path.resolve(__dirname, "..", "..");
const src = JSON.parse(fs.readFileSync(path.join(root, "out", "pm-nfl-events.json"), "utf8"));
const events = Array.isArray(src) ? src : src.events;
const pick = (o, ks) => Object.fromEntries(ks.filter((k) => o[k] !== undefined).map((k) => [k, o[k]]));
const types = new Set(["moneyline", "spreads", "totals"]);
const out = events
  .filter((e) => (e.markets || []).some((m) => m.sportsMarketType === "moneyline"))
  .map((e) => ({
    ...pick(e, ["id", "title", "endDate", "startTime", "live", "ended", "closed"]),
    teams: (e.teams || []).map((t) => pick(t, ["name", "ordering"])),
    markets: e.markets
      .filter((m) => types.has(m.sportsMarketType))
      .map((m) => pick(m, ["id", "question", "groupItemTitle", "line", "outcomes", "outcomePrices", "clobTokenIds",
        "sportsMarketType", "bestBid", "bestAsk", "closed", "archived", "volume", "liquidity", "gameStartTime"])),
  }));
const dst = path.join(__dirname, "pm-nfl-events.json");
fs.writeFileSync(dst, JSON.stringify(out));
console.log(dst, out.length, "events");
