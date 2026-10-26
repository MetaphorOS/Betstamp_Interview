const fs = require('node:fs');
const path = require('node:path');
let playwright;
try { playwright = require('playwright'); } catch { playwright = require('C:/Users/USER/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright'); }
const urls = process.argv.slice(2);
if (!urls.length) throw new Error('Usage: node capture.cjs URL [URL ...]');
const dir = path.resolve(process.env.OUTPUT_DIR || path.join('captures','browser-'+Date.now()));
fs.mkdirSync(dir, {recursive:true});
const tasks = new Set();
const track = p => { tasks.add(p); p.finally(()=>tasks.delete(p)); };
(async()=>{
 const browser = await playwright.chromium.launch({headless:process.env.HEADED !== '1', channel:'chrome'});
 try {
  for (let i=0;i<urls.length;i++) {
   const context = await browser.newContext();
   const page = await context.newPage();
   const prefix=path.join(dir,`${i}-${new URL(urls[i]).hostname}`);
   const log = record => fs.appendFileSync(prefix+'.jsonl',JSON.stringify({receivedAt:new Date().toISOString(),...record})+'\n');
   let n=0;
   page.on('websocket',ws=>{
    log({kind:'ws-open',url:ws.url()});
    ws.on('framesent',f=>log({kind:'ws-sent',url:ws.url(),payload:Buffer.isBuffer(f.payload)?f.payload.toString('base64'):f.payload}));
    ws.on('framereceived',f=>log({kind:'ws-received',url:ws.url(),binary:Buffer.isBuffer(f.payload),payload:Buffer.isBuffer(f.payload)?f.payload.toString('base64'):f.payload}));
    ws.on('close',()=>log({kind:'ws-close',url:ws.url()}));
   });
   page.on('response',response=>track((async()=>{
    const req=response.request();
    const type=req.resourceType();
    const id=n++;
    const record={kind:'http',url:response.url(),status:response.status(),type,method:req.method(),timing:req.timing()};
    if(['xhr','fetch','script','document'].includes(type)) {
     try { const body=await response.body(); const file=prefix+`-${id}.body`; fs.writeFileSync(file,body); record.bodyFile=path.basename(file); record.bytes=body.length; } catch(e) {record.error=e.message;}
    }
    log(record);
   })().catch(e=>log({kind:'capture-error',message:e.message}))));
   try {await page.goto(urls[i],{waitUntil:'domcontentloaded',timeout:45000});} catch(e){log({kind:'navigation-error',message:e.message});}
   await page.waitForTimeout(Number(process.env.CAPTURE_MS||20000));
   console.log(JSON.stringify({url:page.url(),title:await page.title(),text:(await page.locator('body').innerText()).slice(0,700)}));
   await page.screenshot({path:prefix+'.png',fullPage:true});
   await Promise.allSettled([...tasks]);
   await context.close();
  }
 } finally {await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
