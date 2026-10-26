const fs=require('fs');
const meta=JSON.parse(fs.readFileSync(process.argv[2]+'/metadata.json'));
const path=process.argv[2]+'/dk-initial.jsonl';
const log=x=>fs.appendFileSync(path,JSON.stringify({at:new Date().toISOString(),...x})+'\n');
const ws=new WebSocket('wss://sportsbook-ws-ca-on.draftkings.com/websocket?format=json&locale=en');
ws.addEventListener('open',()=>{for(const [g,d]of Object.entries(meta.dk))for(const p of Object.values(d.subscriptionPartials).filter(p=>p.entity==='events')){const {entity,...queryParams}=p;ws.send(JSON.stringify({jsonrpc:'2.0',method:'subscribe',id:g,params:{entity,queryParams:{...queryParams,initialData:true,projection:'sportsbook',locale:'en'},forwardedHeaders:{},clientMetadata:{feature:'leagueSubcategory','X-Client-Name':'web','X-Client-Version':'2643.4.1.10'},jwt:'',siteName:'dkcaon'}}));}});
ws.addEventListener('message',e=>{const m=JSON.parse(e.data);log({body:m});console.log(JSON.stringify({id:m.id,event:m.event,dataKeys:Object.keys(m.data?.data||{}),selectionCounts:Object.fromEntries(Object.entries(m.data?.data||{}).map(([k,v])=>[k,v?.selections?.length]))}));});
ws.addEventListener('error',e=>log({error:e.message}));
setTimeout(()=>{ws.close();setTimeout(()=>process.exit(),2000).unref()},15000);
