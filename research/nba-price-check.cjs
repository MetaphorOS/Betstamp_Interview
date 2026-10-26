const fs=require('fs');
const types=['MONEY_LINE','MATCH_HANDICAP_(2-WAY)','TOTAL_POINTS_(OVER/UNDER)','ALTERNATE_HANDICAPS','ALTERNATE_TOTAL_POINTS'];
(async()=>{
const d=JSON.parse(fs.readFileSync('captures/nba/fd-popular.json'));
const ids=Object.values(d.attachments.markets).filter(m=>types.includes(m.marketType)).map(m=>m.marketId);
const url='https://smp.on.sportsbook.fanduel.ca/api/sports/fixedodds/readonly/v1/getMarketPrices?priceHistory=0';
const r=await fetch(url,{method:'POST',headers:{'Content-Type':'application/json','X-Application':'FhMFpcPWXMeyZxOx'},body:JSON.stringify({marketIds:ids}),signal:AbortSignal.timeout(20000)});
const body=await r.text();fs.writeFileSync('captures/nba/fd-prices.json',body);
fs.writeFileSync('captures/nba/fd-prices.meta.json',JSON.stringify({url,at:new Date().toISOString(),status:r.status,ids},null,2));
console.log(r.status,body.slice(0,2000));
})().catch(console.error);
