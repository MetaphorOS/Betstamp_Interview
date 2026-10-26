const fs=require('node:fs');
const dir=process.argv[2],rows=fs.readFileSync(`${dir}/raw.jsonl`,'utf8').trim().split('\n').map(JSON.parse);
const cache=()=>({events:new Map(),markets:new Map(),selections:new Map()});
const ws=cache(),previous={ws:new Map(),poll:new Map()},moves={ws:[],poll:[]};
let seeded=false,unknownChanges=0;
function snapshot(data){return Object.fromEntries(['events','markets','selections'].map(k=>[k,new Map(data[k].map(x=>[x.id,x]))]));}
function observe(channel,c,r){
 for(const s of c.selections.values()){
  const m=c.markets.get(s.marketId),e=m&&c.events.get(m.eventId);if(!m||!e||!s.outcomeType||!s.displayOdds?.american)continue;
  if(!['Moneyline','Spread','Total'].includes(m.marketType?.name)&&!['Moneyline','Spread','Total'].includes(m.name))continue;
  const price={line:s.points??null,american:Number(s.displayOdds.american.replace('−','-').replace('EVEN','100'))};
  if(!Number.isFinite(price.american))continue;
  const id=`${m.id}/${s.outcomeType}`,signature=JSON.stringify(price),old=previous[channel].get(id);
  if(old!==undefined&&old!==signature)moves[channel].push({id,signature,price,time:r.monoMs,at:r.at,eventId:e.id,event:e.name,live:e.status==='STARTED',selectionId:s.id});
  previous[channel].set(id,signature);
 }
}
for(const r of rows){
 if(r.kind==='poll'&&r.status===200){const c=snapshot(r.body);observe('poll',c,r);if(!seeded){Object.assign(ws,snapshot(r.body));observe('ws',ws,r);seeded=true;}}
 if(r.kind==='ws'&&r.message.event==='update'){
  const d=r.message.data?.data;if(!d)continue;
  for(const k of ['events','markets','selections']){
   for(const x of d.remove?.[k]||[])ws[k].delete(typeof x==='object'?x.id:x);
   for(const x of d.add?.[k]||[])ws[k].set(x.id,x);
   for(const x of d.change?.[k]||[]){const old=ws[k].get(x.id);if(!old){unknownChanges++;continue;}ws[k].set(x.id,{...old,...x,...(x.displayOdds?{displayOdds:{...old.displayOdds,...x.displayOdds}}:{})});}
  }
  observe('ws',ws,r);
 }
}
const used=new Set(),pairs=[];
for(const w of moves.ws){const candidates=moves.poll.map((p,i)=>({p,i})).filter(({p,i})=>!used.has(i)&&p.id===w.id&&p.signature===w.signature&&Math.abs(p.time-w.time)<=15000).sort((a,b)=>Math.abs(a.p.time-w.time)-Math.abs(b.p.time-w.time));if(candidates.length){const {p,i}=candidates[0];used.add(i);pairs.push({...w,pollAt:p.at,wsLeadMs:p.time-w.time,selectionIdsAgree:w.selectionId===p.selectionId});}}
function stats(a){const v=a.map(p=>p.wsLeadMs).sort((a,b)=>a-b);return{matched:a.length,wsFirst:a.filter(p=>p.wsLeadMs>0).length,pollFirst:a.filter(p=>p.wsLeadMs<0).length,min:v[0]??null,p50:v.length?v[Math.ceil(v.length*.5)-1]:null,p95:v.length?v[Math.ceil(v.length*.95)-1]:null,max:v.at(-1)??null};}
const result={polls:rows.filter(r=>r.kind==='poll'&&r.status===200).length,pollErrors:rows.filter(r=>r.kind==='poll-error').length,wsFrames:rows.filter(r=>r.kind==='ws'&&r.message.event==='update').length,wsTransitions:moves.ws.length,pollTransitions:moves.poll.length,unknownChanges,all:stats(pairs),live:stats(pairs.filter(p=>p.live)),pregame:stats(pairs.filter(p=>!p.live)),unmatchedWs:moves.ws.length-pairs.length,unmatchedPoll:moves.poll.length-pairs.length,pairs};
fs.writeFileSync(`${dir}/race.json`,JSON.stringify(result,null,2));fs.writeFileSync(`${dir}/transitions.json`,JSON.stringify(moves,null,2));const {pairs:_,...brief}=result;console.log(JSON.stringify(brief,null,2));
