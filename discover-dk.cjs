const fs=require('node:fs');
(async()=>{
const urls=[
'https://sportsbook-nash.draftkings.com/api/sportscontent/dkcaon/v1/leagues/88808',
'https://sportsbook-nash.draftkings.com/api/sportscontent/dkcaon/v1/leagues/88808/categories/492/subcategories/4518',
'https://sportsbook-nash.draftkings.com/api/sportscontent/dkcaon/v1/leagues/87637'];
for(let i=0;i<urls.length;i++){
const r=await fetch(urls[i],{headers:{Accept:'application/json','Accept-Language':'en-US',Origin:'https://sportsbook.draftkings.com',Referer:'https://sportsbook.draftkings.com/','X-Client-Name':'web','X-Client-Version':'2643.4.1.10'},signal:AbortSignal.timeout(20000)});
const body=await r.text();fs.writeFileSync(`captures/dk-discovery-${i}.json`,body);console.log(JSON.stringify({url:urls[i],status:r.status,bytes:body.length,headers:Object.fromEntries(r.headers)}));
}
})().catch(e=>{console.error(e);process.exitCode=1});
