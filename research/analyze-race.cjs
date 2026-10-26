const fs=require('node:fs');
const dir=process.argv[2];
const raw=fs.readFileSync(`${dir}/raw.jsonl`,'utf8').trim().split('\n').map(JSON.parse);
const states={ws:new Map(),poll:new Map()},moves={ws:[],poll:[]};
for(const r of raw){
 let channel,markets;
 if(r.kind==='poll'&&r.status===200&&Array.isArray(r.body)){channel='poll';markets=r.body;}
 else if(r.kind==='ws'&&r.message?.type==='data'&&r.message.payload?.data?.onUpdateMarketPrice){channel='ws';markets=[r.message.payload.data.onUpdateMarketPrice];}
 else continue;
 for(const m of markets)for(const s of m.runnerDetails||[]){
  const id=`${m.marketId}/${s.selectionId}`;
  const price={line:s.handicap,american:s.winRunnerOdds?.americanDisplayOdds?.americanOddsInt};
  if(price.american===undefined)continue;
  const signature=JSON.stringify(price),prev=states[channel].get(id);
  if(prev!==undefined&&prev!==signature)moves[channel].push({id,signature,price,at:r.at,time:r.monoMs??r.at,marketStatus:m.marketStatus,runnerStatus:s.runnerStatus});
  states[channel].set(id,signature);
 }
}
const used=new Set(),pairs=[];
for(const w of moves.ws){
 const candidates=moves.poll.map((p,i)=>({p,i})).filter(({p,i})=>!used.has(i)&&p.id===w.id&&p.signature===w.signature&&Math.abs(p.time-w.time)<=15000).sort((a,b)=>Math.abs(a.p.time-w.time)-Math.abs(b.p.time-w.time));
 if(candidates.length){const {p,i}=candidates[0];used.add(i);pairs.push({id:w.id,price:w.price,wsAt:w.at,pollAt:p.at,wsLeadMs:p.time-w.time,wsStatus:w.marketStatus,pollStatus:p.marketStatus});}
}
const values=pairs.map(p=>p.wsLeadMs).sort((a,b)=>a-b),quantile=p=>values.length?values[Math.ceil(p*values.length)-1]:null;
const summary={method:'First observation of matching selection price transitions per transport, after each transport has baseline. Matched by market/selection/line/American odds within 15 seconds. Same-process monotonic receive times where present. Positive lead means WebSocket arrived earlier. Price reversions and sampling gaps can make pairing ambiguous; exploratory short run, not source-engine latency.',polls:raw.filter(r=>r.kind==='poll'&&r.status===200).length,wsFrames:raw.filter(r=>r.message?.type==='data').length,wsTransitions:moves.ws.length,pollTransitions:moves.poll.length,matchedTransitions:pairs.length,wsFirst:pairs.filter(p=>p.wsLeadMs>0).length,pollFirst:pairs.filter(p=>p.wsLeadMs<0).length,wsLeadMs:{min:values[0],p50:quantile(.5),p95:quantile(.95),max:values.at(-1)},unmatchedWsTransitions:moves.ws.length-pairs.length,unmatchedPollTransitions:moves.poll.length-pairs.length,pairs};
fs.writeFileSync(`${dir}/race.json`,JSON.stringify(summary,null,2));const {pairs:_,...brief}=summary;console.log(JSON.stringify(brief,null,2));
