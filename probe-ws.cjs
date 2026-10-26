const fs=require('node:fs');
const book=process.argv[2];
if(!['dk','fd'].includes(book))throw new Error('Usage: node probe-ws.cjs dk|fd [--nfl|MARKET_ID ...]');
fs.mkdirSync('captures',{recursive:true});
let received=0;
const log=record=>{const r={at:new Date().toISOString(),...record};fs.appendFileSync(`captures/${book}-ws.jsonl`,JSON.stringify(r)+'\n');if(record.kind!=='received'||++received<=3)console.log(JSON.stringify(r).slice(0,1400));};
let url,protocols,auth,query;
if(book==='dk')url='wss://sportsbook-ws-ca-on.draftkings.com/websocket?format=json&locale=en-US';
else {
 const s=fs.readFileSync('captures/fd-main.js','utf8');
 const key=s.match(/aws_appsync_clientId:"([^"]+)"/)[1];
 auth={Authorization:key,host:'pir.on.sportsbook.fanduel.ca'};
 query=s.match(/subscription OnUpdateMarketPrice\(\$ids: \[String!\]!\) \{[\s\S]+?\n    \}/)[0];
 url='wss://pir.on.sportsbook.fanduel.ca/graphql/realtime?header='+Buffer.from(JSON.stringify(auth)).toString('base64')+'&payload=e30=';
 protocols=['graphql-ws'];
}
const ws=new WebSocket(url,protocols);
const send=x=>{log({kind:'sent',payload:x});ws.send(JSON.stringify(x));};
const timer=setTimeout(()=>{ws.close();setTimeout(()=>process.exit(),2000).unref();},Number(process.env.CAPTURE_MS||45000));
ws.addEventListener('open',()=>{
 log({kind:'open'});
 if(book==='dk')send({jsonrpc:'2.0',method:'subscribe',id:'nfl-events',params:{entity:'events',queryParams:{query:"$filter=leagueId eq '88808'",initialData:false,includeMarkets:"$filter=tags/all(t: t ne 'SportcastBetBuilder')",projection:'sportsbook',locale:'en-US'},forwardedHeaders:{},clientMetadata:{feature:'odds-research','X-Client-Name':'web','X-Client-Version':'unknown'},jwt:'',siteName:'dkcaon'}});
 else send({type:'connection_init'});
});
ws.addEventListener('message',async e=>{
 const text=typeof e.data==='string'?e.data:Buffer.from(await e.data.arrayBuffer()).toString();
 log({kind:'received',payload:text});
 let m;try{m=JSON.parse(text)}catch{return;}
 if(book==='fd'&&m.type==='connection_ack') {
  const ids=process.argv[3]==='--nfl'?Object.values(JSON.parse(fs.readFileSync('captures/fd-nfl.json','utf8')).attachments.markets).filter(m=>['MONEY_LINE','MATCH_HANDICAP_(2-WAY)','TOTAL_POINTS_(OVER/UNDER)'].includes(m.marketType)).map(m=>m.marketId):process.argv.slice(3);
  if(ids.length)send({id:'nfl-prices',type:'start',payload:{data:JSON.stringify({query,variables:{ids}}),extensions:{authorization:auth}}});
 }
});
ws.addEventListener('error',e=>log({kind:'error',message:e.message}));
ws.addEventListener('close',e=>{clearTimeout(timer);log({kind:'close',code:e.code,reason:e.reason});});
