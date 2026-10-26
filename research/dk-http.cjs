const DEFAULT_USER_AGENT='Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36';
const DEFAULT_URL='https://sportsbook-nash.draftkings.com/api/sportscontent/dkcaon/v1/leagues/88808';
async function fetchSnapshot({url=DEFAULT_URL,userAgent=process.env.DK_USER_AGENT||DEFAULT_USER_AGENT}={}){
 const start=performance.now();
 const response=await fetch(url,{headers:{'User-Agent':userAgent,Accept:'application/json'},signal:AbortSignal.timeout(15000)});
 const text=await response.text();
 if(!response.ok)throw new Error(`DraftKings HTTP ${response.status}: ${text.slice(0,160)}`);
 const data=JSON.parse(text);
 for(const field of ['events','markets','selections'])if(!Array.isArray(data[field]))throw new Error(`Invalid snapshot: missing ${field}`);
 return{data,receivedMonoMs:performance.now(),receivedAt:Date.now(),elapsedMs:performance.now()-start,cacheControl:response.headers.get('cache-control'),age:response.headers.get('age')};
}
module.exports={fetchSnapshot,DEFAULT_USER_AGENT,DEFAULT_URL};
if(require.main===module)fetchSnapshot().then(({data,...meta})=>console.log(JSON.stringify({...meta,events:data.events.length,markets:data.markets.length,selections:data.selections.length}))).catch(e=>{console.error(e.message);process.exitCode=1});
