const fs=require('node:fs');
const sleep=ms=>new Promise(r=>setTimeout(r,ms));
(async()=>{
 const dir=`captures/comparison-${Date.now()}`;fs.mkdirSync(dir,{recursive:true});
 const records=[];const log=x=>{const row={at:Date.now(),monoMs:performance.now(),...x};records.push(row);fs.appendFileSync(`${dir}/raw.jsonl`,JSON.stringify(row)+'\n');};
 const main=fs.readFileSync('captures/fd-main.js','utf8');
 const auth={Authorization:main.match(/aws_appsync_clientId:"([^"]+)"/)[1],host:'pir.on.sportsbook.fanduel.ca'};
 const query=main.match(/subscription OnUpdateMarketPrice\(\$ids: \[String!\]!\) \{[\s\S]+?\n    \}/)[0];
 const discovery=await fetch('https://sbapi.on.sportsbook.fanduel.ca/api/in-play?_ak=FhMFpcPWXMeyZxOx&includeTabs=true',{signal:AbortSignal.timeout(20000)});
 if(!discovery.ok)throw Error(`Discovery ${discovery.status}`);
 const board=await discovery.json();fs.writeFileSync(`${dir}/discovery.json`,JSON.stringify(board));
 const markets=Object.values(board.attachments.markets).filter(m=>m.inPlay&&['MONEY_LINE','MATCH_HANDICAP_(2-WAY)','TOTAL_POINTS_(OVER/UNDER)'].includes(m.marketType)).slice(0,18);
 if(!markets.length)throw Error('No live main markets');
 const ids=markets.map(m=>m.marketId);console.log(JSON.stringify({dir,markets:markets.map(m=>({id:m.marketId,type:m.marketType,event:board.attachments.events[m.eventId]?.name}))}));
 const ws=new WebSocket('wss://pir.on.sportsbook.fanduel.ca/graphql/realtime?header='+Buffer.from(JSON.stringify(auth)).toString('base64')+'&payload=e30=',['graphql-ws']);
 ws.addEventListener('open',()=>ws.send(JSON.stringify({type:'connection_init'})));
 ws.addEventListener('message',e=>{const m=JSON.parse(e.data);log({kind:'ws',message:m});if(m.type==='connection_ack')ws.send(JSON.stringify({id:'compare',type:'start',payload:{data:JSON.stringify({query,variables:{ids}}),extensions:{authorization:auth}}}));});
 ws.addEventListener('error',e=>log({kind:'ws-error',message:e.message}));
 const until=performance.now()+Number(process.env.CAPTURE_MS||90000);
 while(performance.now()<until){const start=Date.now();try{
  const r=await fetch('https://smp.on.sportsbook.fanduel.ca/api/sports/fixedodds/readonly/v1/getMarketPrices?priceHistory=0',{method:'POST',headers:{'Content-Type':'application/json','X-Application':'FhMFpcPWXMeyZxOx'},body:JSON.stringify({marketIds:ids}),signal:AbortSignal.timeout(10000)});
  const body=await r.json();log({kind:'poll',start,status:r.status,body});if(r.status===429)break;
 }catch(e){log({kind:'poll-error',message:e.message});}await sleep(Math.max(0,5000-(Date.now()-start)));}
 ws.close();
 const norm=m=>({marketId:String(m.marketId),inplay:m.inplay,marketStatus:m.marketStatus,runners:(m.runnerDetails||[]).map(r=>({id:String(r.selectionId),line:r.handicap,status:r.runnerStatus,american:r.winRunnerOdds?.americanDisplayOdds?.americanOddsInt,decimal:r.winRunnerOdds?.trueOdds?.decimalOdds?.decimalOdds})).sort((a,b)=>a.id.localeCompare(b.id))});
 const equal=(a,b)=>a.marketId===b.marketId&&a.inplay===b.inplay&&a.marketStatus===b.marketStatus&&a.runners.length===b.runners.length&&a.runners.every((r,i)=>{const s=b.runners[i];return r.id===s.id&&r.line===s.line&&r.status===s.status&&r.american===s.american;});
 const polls=records.filter(r=>r.kind==='poll'&&r.status===200&&Array.isArray(r.body));
 const pushes=records.filter(r=>r.kind==='ws'&&r.message.type==='data'&&r.message.payload?.data?.onUpdateMarketPrice).map(r=>({at:r.at,value:norm(r.message.payload.data.onUpdateMarketPrice)}));
 const comparisons=pushes.map(p=>{
  const samples=polls.flatMap(r=>r.body.filter(m=>String(m.marketId)===p.value.marketId).map(m=>({at:r.at,start:r.start,value:norm(m)})));
  const nearest=samples.toSorted((a,b)=>Math.abs(a.at-p.at)-Math.abs(b.at-p.at))[0];
  const match=samples.filter(s=>equal(p.value,s.value)).toSorted((a,b)=>Math.abs(a.at-p.at)-Math.abs(b.at-p.at))[0];
  const after=samples.find(s=>s.start>=p.at);
  return{wsAt:p.at,ws:p.value,nearestPoll:nearest,nearestExact:nearest?equal(p.value,nearest.value):false,nextPollMatches:after?equal(p.value,after.value):null,matchingPoll:match,matchingPollOffsetMs:match?match.at-p.at:null};
 });
 const summary={dir,markets:ids.length,successfulPolls:polls.length,pushFrames:pushes.length,marketsWithPush:new Set(pushes.map(p=>p.value.marketId)).size,nearestExact:comparisons.filter(c=>c.nearestExact).length,matchInAnyPoll:comparisons.filter(c=>c.matchingPoll).length,nextPollMatches:comparisons.filter(c=>c.nextPollMatches===true).length,unmatched:comparisons.filter(c=>!c.matchingPoll).length,comparisons};
 fs.writeFileSync(`${dir}/comparison.json`,JSON.stringify(summary,null,2));const {comparisons:_,...brief}=summary;console.log(JSON.stringify(brief));
 setTimeout(()=>process.exit(),2000).unref();
})().catch(e=>{console.error(e);process.exitCode=1;});
