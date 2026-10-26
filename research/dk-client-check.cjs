const fs=require('node:fs'),https=require('node:https'),http2=require('node:http2');
const url='https://sportsbook-nash.draftkings.com/api/sportscontent/dkcaon/v1/leagues/88808';
const headers={Accept:'application/json','User-Agent':'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36',Origin:'https://sportsbook.draftkings.com',Referer:'https://sportsbook.draftkings.com/'};
const save=(mode,status,h,b)=>{fs.writeFileSync(`captures/dk-client-${mode}.json`,b);console.log(JSON.stringify({mode,node:process.version,status,bytes:b.length,headers:h}));};
(async()=>{
const r=await fetch(url,{headers,signal:AbortSignal.timeout(15000)});save('fetch-headers',r.status,Object.fromEntries(r.headers),await r.text());
await new Promise(resolve=>{const req=https.get(url,{headers,timeout:15000},r=>{let b='';r.on('data',x=>b+=x);r.on('end',()=>{save('https',r.statusCode,r.headers,b);resolve()});});req.on('timeout',()=>req.destroy());req.on('error',e=>{console.log(e.message);resolve()});});
await new Promise(resolve=>{const c=http2.connect(new URL(url).origin);c.on('error',e=>{console.log('h2',e.message);resolve()});const q=c.request({':path':new URL(url).pathname,...Object.fromEntries(Object.entries(headers).map(([k,v])=>[k.toLowerCase(),v]))});let b='',h;q.setTimeout(15000,()=>{q.close();c.close();resolve()});q.on('response',x=>h=x);q.on('data',x=>b+=x);q.on('end',()=>{save('h2',h?.[':status'],h,b);c.close();resolve()});q.on('error',e=>{console.log('h2 request',e.message);c.close();resolve()});});
})().catch(console.error);
