// Observe only the local board; does not send additional requests to sportsbooks.
const fs = require('node:fs');
const path = require('node:path');
const out = path.join(__dirname, '.gotmp', 'soak');
fs.mkdirSync(out, {recursive:true});
const start = Date.now(), deadline = start + 20*60*1000;
const controller = new AbortController();
const seen = new Map();
const counts = {slotDeltas:0, propDeltas:0, streamSlotDeltas:0, streamPropDeltas:0, confirmations:0, priceChanges:0, propPriceChanges:0};
let latest, stopped=false, samples=0;
function save(name,x){fs.appendFileSync(path.join(out,name),JSON.stringify(x)+'\n');}
function price(e){return e ? JSON.stringify([e.line,e.american,e.decimal,e.status]) : null;}
async function stream(){
 while(!stopped){
  try {
   const r=await fetch('http://127.0.0.1:8080/stream',{signal:controller.signal});
   if(!r.ok) throw new Error('SSE HTTP '+r.status);
   let buf='', decoder=new TextDecoder();
   for await(const chunk of r.body){
    buf+=decoder.decode(chunk,{stream:true});let pos;
    while((pos=buf.indexOf('\n\n'))>=0){
     const block=buf.slice(0,pos);buf=buf.slice(pos+2);
     const data=block.split('\n').filter(x=>x.startsWith('data:')).map(x=>x.slice(5).trim()).join('\n');
     if(!data)continue;
     let d;try{d=JSON.parse(data)}catch{continue}
     if(d.slots){for(const s of d.slots)if(s.entries?.betmgm)seen.set(s.gameId+'|'+s.identity,price(s.entries.betmgm));continue;}
     if(d.book!=='betmgm')continue;
     save('events.jsonl',d);
     if(d.kind==='confirm')counts.confirmations+=Object.keys(d.confirms||{}).length;
     if(d.kind==='slot'){
      const prop=d.identity?.startsWith('player_prop|');
      counts.slotDeltas++;if(prop)counts.propDeltas++;
      if(d.source==='stream'){counts.streamSlotDeltas++;if(prop)counts.streamPropDeltas++;}
      const key=d.gameId+'|'+d.identity, value=price(d.entry);
      if(seen.has(key)&&seen.get(key)!==value){counts.priceChanges++;if(prop)counts.propPriceChanges++;}
      if(d.removed)seen.delete(key);else seen.set(key,value);
     }
    }
   }
  }catch(e){if(!stopped)save('errors.jsonl',{at:new Date(),error:String(e)});}
  if(!stopped)await new Promise(r=>setTimeout(r,1000));
 }
}
async function sample(){
 try{
  const b=await (await fetch('http://127.0.0.1:8080/api/board')).json();
  const slots=b.slots.filter(s=>s.entries?.betmgm), props=slots.filter(s=>s.family==='player_prop');
  latest={at:new Date(),elapsedSec:Math.round((Date.now()-start)/1000),health:b.health.betmgm,slots:slots.length,props:props.length,counts:{...counts}};
  save('samples.jsonl',latest);
  fs.writeFileSync(path.join(out,'latest-board.json'),JSON.stringify({games:b.games,slots,health:b.health.betmgm}));
  if(samples++%2===0)console.log(JSON.stringify(latest));
  if(Date.now()-start>=16*60*1000&&b.health.betmgm.snapshots>=4)stopped=true;
 }catch(e){save('errors.jsonl',{at:new Date(),error:String(e)})}
 if(Date.now()>=deadline)stopped=true;
 if(stopped){controller.abort();fs.writeFileSync(path.join(out,'summary.json'),JSON.stringify(latest,null,2));console.log('FINISHED '+JSON.stringify(latest));return;}
 setTimeout(sample,30000);
}
stream();sample();
