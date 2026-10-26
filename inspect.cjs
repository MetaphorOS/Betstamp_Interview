const fs=require('node:fs');
const s=fs.readFileSync(process.argv[2],'utf8');
const expression=process.argv[3] || 'websocket|sportsbook-nash|wss:|appsync|content-managed-page|getMarketPrices';
for(const m of s.matchAll(/<script[^>]+src="([^"]+)/g)) console.log('SCRIPT',m[1]);
for(const m of s.matchAll(new RegExp('.{0,160}(?:'+expression+').{0,220}','gi'))) console.log(m[0]);
