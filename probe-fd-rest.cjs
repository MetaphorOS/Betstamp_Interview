const fs=require('node:fs');
(async()=>{
const main=fs.readFileSync('captures/fd-main.js','utf8');
const key=main.match(/aws_appsync_clientId:"([^"]+)"/)[1];
const query=main.match(/query GetMarketPrices\(\$ids: \[String\]!\) \{[\s\S]+?\n    \}/)[0];
const markets=Object.values(JSON.parse(fs.readFileSync('captures/fd-nfl.json','utf8')).attachments.markets).filter(m=>['MONEY_LINE','MATCH_HANDICAP_(2-WAY)','TOTAL_POINTS_(OVER/UNDER)'].includes(m.marketType));
for (const mode of ['graphql','smp']) {
 const url=mode==='graphql'?'https://pir.on.sportsbook.fanduel.ca/graphql':'https://smp.on.sportsbook.fanduel.ca/api/sports/fixedodds/readonly/v1/getMarketPrices?priceHistory=0';
 const ids=markets.slice(0,3).map(m=>m.marketId);
 const start=performance.now();
 const r=await fetch(url,{method:'POST',headers:{'Content-Type':'application/json',...(mode==='graphql'?{Authorization:key}:{'X-Application':'FhMFpcPWXMeyZxOx'})},body:JSON.stringify(mode==='graphql'?{query,variables:{ids}}:{marketIds:ids}),signal:AbortSignal.timeout(20000)});
 const body=await r.text();fs.writeFileSync(`captures/fd-${mode}.json`,body);
 const meta={url,status:r.status,elapsedMs:performance.now()-start,headers:Object.fromEntries(r.headers),bytes:Buffer.byteLength(body),ids};
 fs.writeFileSync(`captures/fd-${mode}.meta.json`,JSON.stringify(meta,null,2));console.log(JSON.stringify(meta));console.log(body.slice(0,800));
}
})().catch(e=>{console.error(e);process.exitCode=1});
