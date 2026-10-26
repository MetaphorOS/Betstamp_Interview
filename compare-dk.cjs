const fs=require('node:fs');
const {fetchSnapshot}=require('./dk-http.cjs');
const sleep=ms=>new Promise(r=>setTimeout(r,ms));
(async()=>{
const dir=`captures/dk-race-${Date.now()}`;fs.mkdirSync(dir,{recursive:true});
const log=x=>fs.appendFileSync(`${dir}/raw.jsonl`,JSON.stringify({at:Date.now(),monoMs:performance.now(),...x})+'\n');
const first=await fetchSnapshot();fs.writeFileSync(`${dir}/bootstrap.json`,JSON.stringify(first.data));
log({kind:'poll',status:200,body:first.data,monoMs:first.receivedMonoMs,at:first.receivedAt});
const partials=Object.values(first.data.subscriptionPartials||{});
if(!partials.length)throw new Error('No subscription filters in snapshot');
console.log(JSON.stringify({dir,events:first.data.events.length,live:first.data.events.filter(e=>e.status==='STARTED').map(e=>({id:e.id,name:e.name})),markets:first.data.markets.length}));
const ws=new WebSocket('wss://sportsbook-ws-ca-on.draftkings.com/websocket?format=json&locale=en');
ws.addEventListener('open',()=>{for(const [i,p] of partials.entries()){const {entity,...queryParams}=p;ws.send(JSON.stringify({jsonrpc:'2.0',method:'subscribe',id:`race-${i}`,params:{entity,queryParams:{...queryParams,initialData:false,projection:'sportsbook'},forwardedHeaders:{},clientMetadata:{feature:'leagueSubcategory','X-Client-Name':'web','X-Client-Version':'2643.4.1.10'},jwt:'',siteName:'dkcaon'}}));}});
ws.addEventListener('message',e=>{try{log({kind:'ws',message:JSON.parse(e.data)})}catch{log({kind:'parse-error'})}});
ws.addEventListener('error',e=>log({kind:'ws-error',message:e.message}));
ws.addEventListener('close',e=>log({kind:'ws-close',code:e.code}));
const end=performance.now()+Number(process.env.CAPTURE_MS||120000),interval=Number(process.env.POLL_MS||1000);
let polls=1,errors=0;
while(performance.now()<end){const start=performance.now();try{const s=await fetchSnapshot();log({kind:'poll',status:200,body:s.data,monoMs:s.receivedMonoMs,at:s.receivedAt,rttMs:s.elapsedMs,cacheControl:s.cacheControl,age:s.age});polls++;}catch(e){log({kind:'poll-error',message:e.message});errors++;if(/403|429/.test(e.message))break;}await sleep(Math.max(0,interval-(performance.now()-start)));}
ws.close();console.log(JSON.stringify({dir,polls,errors,interval}));setTimeout(()=>process.exit(),2000).unref();
})().catch(e=>{console.error(e);process.exitCode=1});
