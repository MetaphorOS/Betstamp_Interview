const fs=require('node:fs');
(async()=>{
 const [url,file]=process.argv.slice(2);
 const start=performance.now();
 const r=await fetch(url,{signal:AbortSignal.timeout(30000)});
 const body=Buffer.from(await r.arrayBuffer());
 fs.writeFileSync(file,body);
 const meta={url:r.url,status:r.status,elapsedMs:performance.now()-start,bytes:body.length,headers:Object.fromEntries(r.headers),receivedAt:new Date().toISOString()};
 fs.writeFileSync(file+'.meta.json',JSON.stringify(meta,null,2));
 console.log(JSON.stringify(meta));
})().catch(e=>{console.error(e);process.exitCode=1});
