const fs=require('node:fs');
const source=JSON.parse(fs.readFileSync('captures/fd-nfl.json','utf8'));
const types={'MONEY_LINE':'moneyline','MATCH_HANDICAP_(2-WAY)':'spread','TOTAL_POINTS_(OVER/UNDER)':'total'};
const rows=[];
for(const m of Object.values(source.attachments.markets)) {
 if(!types[m.marketType])continue;
 const e=source.attachments.events[m.eventId];
 if(!e)throw new Error(`Missing event ${m.eventId}`);
 for(const r of m.runners)rows.push({book:'fanduel',region:'ON',eventId:String(e.eventId),event:e.name,start:e.openDate,marketId:m.marketId,market:types[m.marketType],selectionId:String(r.selectionId),side:r.result?.type,label:r.runnerName,line:types[m.marketType]==='moneyline'?null:r.handicap,american:r.winRunnerOdds?.americanDisplayOdds?.americanOddsInt??null,decimal:r.winRunnerOdds?.trueOdds?.decimalOdds?.decimalOdds??null,inPlay:m.inPlay,marketStatus:m.marketStatus,runnerStatus:r.runnerStatus});
}
fs.writeFileSync('captures/fd-nfl-normalized.json',JSON.stringify(rows,null,2));
console.log(JSON.stringify({games:new Set(rows.map(r=>r.eventId)).size,markets:new Set(rows.map(r=>r.marketId)).size,selections:rows.length,missingPrices:rows.filter(r=>r.american===null).length}));
