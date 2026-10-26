const fs=require('node:fs');
(async()=>{
const req=JSON.parse(fs.readFileSync('captures/dk-browser-request.json','utf8'));
const h=Object.fromEntries(Object.entries(req.headers).filter(([k])=>!(/trace|datadog/i.test(k))));
const plus=JSON.parse(fs.readFileSync('captures/dk-current.meta.json','utf8')).url;
const legacy='https://sportsbook-nash.draftkings.com/api/sportscontent/dkcaon/v1/leagues/88808';
const cases=[['full',req.url,h],['no-headers',req.url,{}],['plus-encoding',plus,h],['legacy-full',legacy,h],['only-product',req.url,Object.fromEntries(Object.entries(h).filter(([k])=>/^(x-|content-type)/i.test(k)))],['only-user-agent',req.url,{'User-Agent':h['User-Agent']}],['full-repeat',req.url,h]];
const out=[];
for(const [name,url,headers] of cases){const start=performance.now();const r=await fetch(url,{headers,signal:AbortSignal.timeout(15000)});const body=await r.text();let counts;try{const j=JSON.parse(body);counts={events:j.events?.length,markets:j.markets?.length,selections:j.selections?.length}}catch{}const record={name,url,status:r.status,bytes:body.length,elapsedMs:performance.now()-start,counts};out.push(record);console.log(JSON.stringify(record));fs.writeFileSync(`captures/dk-ablation-${name}.json`,body);await new Promise(r=>setTimeout(r,1000));}
fs.writeFileSync('captures/dk-ablation-summary.json',JSON.stringify(out,null,2));
})().catch(console.error);
