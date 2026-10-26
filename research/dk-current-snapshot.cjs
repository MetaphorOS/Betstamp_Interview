const fs=require('node:fs');
const state=JSON.parse(fs.readFileSync('captures/dk-initial-state.json','utf8'));
const params=state.cmsConfigurations.template.data.sets.find(x=>x.name==='5TAMW1XData').parameters;
const query=new URLSearchParams(Object.fromEntries(Object.entries(params).map(([k,v])=>[k,v.replaceAll('@leagueId','88808')])));
const url='https://sportsbook-nash.draftkings.com/sites/CA-ON-SB/api/sportscontent/controldata/league/leagueSubcategory/v1/markets?'+query;
(async()=>{const r=await fetch(url,{headers:{Accept:'application/json','X-Client-Name':'web','X-Client-Version':'2643.4.1.10'},signal:AbortSignal.timeout(20000)});const body=await r.text();fs.writeFileSync('captures/dk-current.json',body);const meta={url,status:r.status,headers:Object.fromEntries(r.headers),bytes:body.length};fs.writeFileSync('captures/dk-current.meta.json',JSON.stringify(meta,null,2));console.log(JSON.stringify(meta));})().catch(console.error);
