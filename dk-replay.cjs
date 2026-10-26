const fs=require('node:fs');
(async()=>{
const req=JSON.parse(fs.readFileSync('captures/dk-browser-request.json','utf8'));
const headers=Object.fromEntries(Object.entries(req.headers).filter(([k])=>!(/trace|datadog/i.test(k))));
const r=await fetch(req.url,{headers,signal:AbortSignal.timeout(20000)});const body=await r.text();fs.writeFileSync('captures/dk-replay.json',body);console.log(JSON.stringify({status:r.status,bytes:body.length,headers:Object.fromEntries(r.headers)}));
})().catch(console.error);
