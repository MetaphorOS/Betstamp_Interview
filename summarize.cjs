const fs=require('node:fs');
for(const book of ['dk','fd']) {
 const file=`captures/${book}-ws.jsonl`;
 if(!fs.existsSync(file))continue;
 const rows=fs.readFileSync(file,'utf8').trim().split('\n').map(JSON.parse);
 const counts={},events=new Map(),markets=new Map(),selections=new Map(),lags=[];
 let bytes=0,firstData,lastData;
 for(const row of rows) {
  if(row.kind!=='received')continue;
  const m=JSON.parse(row.payload),kind=m.event||m.type;
  counts[kind]=(counts[kind]||0)+1;bytes+=Buffer.byteLength(row.payload);
  if(kind==='update'||kind==='data'){firstData??=row.at;lastData=row.at;}
  const d=m.data?.data;
  if(d)for(const op of ['add','change','remove'])for(const [entity,map] of [['events',events],['markets',markets],['selections',selections]])for(const item of d[op]?.[entity]||[]){if(op==='remove')map.delete(typeof item==='object'?item.id:item);else map.set(item.id,{...map.get(item.id),...item});}
  if(m.websocketPublishTimestamp)lags.push(Date.parse(row.at)-Date.parse(m.websocketPublishTimestamp));
 }
 lags.sort((a,b)=>a-b);
 const result={book,counts,bytes,firstData,lastData,partialCache:{events:events.size,markets:markets.size,selections:selections.size},uncorrectedPublishToReceiveMs:lags.length?{min:lags[0],median:lags[Math.floor(lags.length/2)],max:lags.at(-1)}:null};
 console.log(JSON.stringify(result,null,2));
 fs.writeFileSync(`captures/${book}-summary.json`,JSON.stringify(result,null,2));
 if(book==='dk')fs.writeFileSync('captures/dk-partial-cache.json',JSON.stringify({events:[...events.values()],markets:[...markets.values()],selections:[...selections.values()]},null,2));
}
