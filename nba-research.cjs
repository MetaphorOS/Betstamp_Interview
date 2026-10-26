const fs=require('node:fs');
const {DEFAULT_USER_AGENT}=require('./dk-http.cjs');
fs.mkdirSync('captures/nba',{recursive:true});
async function get(name,url){const t=performance.now();const r=await fetch(url,{headers:{'User-Agent':DEFAULT_USER_AGENT,Accept:'application/json'},signal:AbortSignal.timeout(25000)});const text=await r.text();fs.writeFileSync(`captures/nba/${name}.json`,text);fs.writeFileSync(`captures/nba/${name}.meta.json`,JSON.stringify({url,status:r.status,at:new Date().toISOString(),elapsedMs:performance.now()-t,bytes:Buffer.byteLength(text),headers:Object.fromEntries([...r.headers].filter(([k])=>k!=='set-cookie'))},null,2));console.log(name,r.status,text.length);return r.ok?JSON.parse(text):null;}
module.exports={get};
if(require.main===module)(async()=>{await get('dk-main','https://sportsbook-nash.draftkings.com/api/sportscontent/dkcaon/v1/leagues/42648');await get('fd-main','https://sbapi.on.sportsbook.fanduel.ca/api/content-managed-page?page=CUSTOM&customPageId=nba&includePrices=true&_ak=FhMFpcPWXMeyZxOx');})().catch(console.error);
