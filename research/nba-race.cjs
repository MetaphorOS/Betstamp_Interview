const fs=require('node:fs');
const {DEFAULT_USER_AGENT}=require('./dk-http.cjs');
const dir=`captures/nba-race-${Date.now()}`;fs.mkdirSync(dir,{recursive:true});
const log=x=>fs.appendFileSync(`${dir}/raw.jsonl`,JSON.stringify({at:Date.now(),t:performance.now(),...x})+'\n');
const sleep=ms=>new Promise(r=>setTimeout(r,ms));
const types=['MONEY_LINE','MATCH_HANDICAP_(2-WAY)','TOTAL_POINTS_(OVER/UNDER)','ALTERNATE_HANDICAPS','ALTERNATE_TOTAL_POINTS'];
async function request(book,group,url,body){const start=performance.now();const r=await fetch(url,{method:body?'POST':'GET',headers:{'User-Agent':DEFAULT_USER_AGENT,Accept:'application/json',...(body?{'Content-Type':'application/json','X-Application':'FhMFpcPWXMeyZxOx'}:{})},body:body&&JSON.stringify(body),signal:AbortSignal.timeout(15000)});const text=await r.text();const t=performance.now();let data;try{data=JSON.parse(text)}catch{data=text}log({book,group,kind:'poll',status:r.status,t,rtt:t-start,age:r.headers.get('age'),body:data});if(!r.ok)throw Error(`HTTP ${r.status}`);return data;}
(async()=>{
 console.log(dir);
 const dkBase='https://sportsbook-nash.draftkings.com/api/sportscontent/dkcaon/v1/leagues/42648';
 const urls={main:dkBase,spread:dkBase+'/categories/487/subcategories/13202',total:dkBase+'/categories/487/subcategories/13201'};
 const dk={};for(const [g,u] of Object.entries(urls))dk[g]=await request('dk',g,u);
 const board=await request('fd','discovery','https://sbapi.on.sportsbook.fanduel.ca/api/content-managed-page?page=CUSTOM&customPageId=nba&includePrices=true&_ak=FhMFpcPWXMeyZxOx');
 const events=Object.values(board.attachments.events).filter(e=>String(e.competitionId)==='10547864'&&Object.values(board.attachments.markets).some(m=>m.eventId===e.eventId&&types.includes(m.marketType))).sort((a,b)=>new Date(a.openDate)-new Date(b.openDate)).slice(0,7);
 const markets={};for(const e of events){const d=await request('fd','discovery',`https://sbapi.on.sportsbook.fanduel.ca/api/event-page?_ak=FhMFpcPWXMeyZxOx&eventId=${e.eventId}&tab=popular`);for(const m of Object.values(d.attachments?.markets||{}))if(types.includes(m.marketType))markets[m.marketId]=m;}
 const ids=Object.keys(markets);if(!ids.length)throw Error('No FD markets');
 fs.writeFileSync(`${dir}/metadata.json`,JSON.stringify({dk,fd:markets,events,intervalMs:1000,durationMs:Number(process.env.CAPTURE_MS||300000)},null,2));
 const bundle=fs.readFileSync('captures/fd-main.js','utf8');const auth={Authorization:bundle.match(/aws_appsync_clientId:"([^"]+)"/)[1],host:'pir.on.sportsbook.fanduel.ca'};
 const query=bundle.match(/subscription OnUpdateMarketPrice\(\$ids: \[String!\]!\) \{[\s\S]+?\n    \}/)[0];
 const sockets=[];
 function socket(book,url,protocol,onopen,onmessage){const ws=new WebSocket(url,protocol);sockets.push(ws);ws.addEventListener('open',()=>{log({book,kind:'open'});onopen(ws)});ws.addEventListener('message',e=>{const t=performance.now();let m;try{m=JSON.parse(e.data)}catch{return}log({book,kind:'ws',t,body:m});onmessage?.(ws,m)});ws.addEventListener('error',e=>log({book,kind:'error',message:e.message}));ws.addEventListener('close',e=>log({book,kind:'close',code:e.code}));}
 socket('dk','wss://sportsbook-ws-ca-on.draftkings.com/websocket?format=json&locale=en',undefined,ws=>{for(const [g,d] of Object.entries(dk))for(const p of Object.values(d.subscriptionPartials).filter(p=>p.entity==='events')){const {entity,...q}=p;ws.send(JSON.stringify({jsonrpc:'2.0',method:'subscribe',id:g,params:{entity,queryParams:{...q,initialData:false,projection:'sportsbook',locale:'en'},forwardedHeaders:{},clientMetadata:{feature:'leagueSubcategory','X-Client-Name':'web','X-Client-Version':'2643.4.1.10'},jwt:'',siteName:'dkcaon'}}));}});
 socket('fd','wss://pir.on.sportsbook.fanduel.ca/graphql/realtime?header='+Buffer.from(JSON.stringify(auth)).toString('base64')+'&payload=e30=',['graphql-ws'],ws=>ws.send(JSON.stringify({type:'connection_init'})),(ws,m)=>{if(m.type==='connection_ack')ws.send(JSON.stringify({id:'nba',type:'start',payload:{data:JSON.stringify({query,variables:{ids}}),extensions:{authorization:auth}}}));});
 const end=performance.now()+Number(process.env.CAPTURE_MS||300000);
 async function loop(book,g,u,b){while(performance.now()<end){const t=performance.now();try{await request(book,g,u,b)}catch(e){log({book,group:g,kind:'poll-error',message:e.message});if(/403|429/.test(e.message))break;}await sleep(Math.max(0,1000-(performance.now()-t)));}}
 console.log(JSON.stringify({fdMarkets:ids.length,events:events.map(e=>e.name),dkMarkets:Object.fromEntries(Object.entries(dk).map(([g,d])=>[g,d.markets.length]))}));
 await Promise.all([...Object.entries(urls).map(([g,u])=>loop('dk',g,u)),loop('fd','prices','https://smp.on.sportsbook.fanduel.ca/api/sports/fixedodds/readonly/v1/getMarketPrices?priceHistory=0',{marketIds:ids})]);
 sockets.forEach(s=>s.close());console.log('Finished '+dir);setTimeout(()=>process.exit(),2000).unref();
})().catch(e=>{console.error(e);process.exitCode=1});
