import {spawn} from 'node:child_process';
import {mkdir, rm, writeFile} from 'node:fs/promises';
import path from 'node:path';

const args = Object.fromEntries(process.argv.slice(2).map(x => { const i=x.indexOf('='); return [x.slice(0,i),x.slice(i+1)]; }));
const turns = Number(args.turns || 10), timeoutMs = Number(args.timeout_ms || 1800000);
const output = args.output;
if (!output) throw new Error('output=<path> is required');
const edge = process.env.EDGE_PATH || 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe';
const debugPort = Number(args.debug_port || 9223);
const profile = path.resolve('runtime', `phase3b-edge-${process.pid}`);
await mkdir(profile, {recursive:true});
const wavs = args.wavs || '/runtime/phase3-short-ja.wav,/runtime/diagnostic-input.wav,/runtime/phase3-long-ja.wav,/runtime/phase3-en.wav';
const targetURL = `http://127.0.0.1:8080/scripts/diagnostics/perf-browser.html?turns=${turns}&between_ms=${Number(args.between_ms || 250)}&wavs=${encodeURIComponent(wavs)}`;
const browser = spawn(edge, ['--headless=new','--autoplay-policy=no-user-gesture-required','--no-first-run',
  `--remote-debugging-port=${debugPort}`,`--user-data-dir=${profile}`,targetURL], {stdio:'ignore', windowsHide:true});
const sleep = ms => new Promise(r=>setTimeout(r,ms));
let ws;
try {
  let page, deadline=Date.now()+30000;
  while(Date.now()<deadline){
    try { const pages=await (await fetch(`http://127.0.0.1:${debugPort}/json`)).json(); page=pages.find(x=>x.url.includes('perf-browser.html')); if(page) break; } catch {}
    await sleep(250);
  }
  if(!page) throw new Error('performance page did not open');
  ws = new WebSocket(page.webSocketDebuggerUrl);
  await new Promise((resolve,reject)=>{ws.onopen=resolve;ws.onerror=reject;});
  let id=0, pending=new Map();
  ws.onmessage=e=>{const m=JSON.parse(e.data);if(m.id&&pending.has(m.id)){pending.get(m.id)(m);pending.delete(m.id);}};
  const evaluate=expression=>new Promise(resolve=>{const n=++id;pending.set(n,resolve);ws.send(JSON.stringify({id:n,method:'Runtime.evaluate',params:{expression,returnByValue:true}}));});
  deadline=Date.now()+timeoutMs; let result;
  while(Date.now()<deadline){
    const message=await evaluate('JSON.stringify(window.__perfResult || null)');
    const value=message.result?.result?.value;
    if(value&&value!=='null'){
      result=JSON.parse(value);
      const complete=result.turns?.length===turns && result.turns.every(t=>t.playback_complete_ms!=null||t.error||t.cancelled);
      process.stdout.write(`\rBrowser benchmark ${result.turns?.length||0}/${turns}`);
      if(complete) break;
    }
    await sleep(1000);
  }
  process.stdout.write('\n');
  if(!result||result.turns?.length!==turns) {
    const partialOutput = `${output}.partial.json`;
    if(result) await writeFile(partialOutput, JSON.stringify(result,null,2));
    throw new Error(`benchmark timeout at ${result?.turns?.length||0}/${turns}; partial=${result ? partialOutput : 'unavailable'}`);
  }
  await writeFile(output, JSON.stringify(result,null,2));
} finally {
  try { ws?.close(); } catch {}
  browser.kill();
  await new Promise(r=>browser.once('exit',r)).catch(()=>{});
  await rm(profile,{recursive:true,force:true,maxRetries:5,retryDelay:200}).catch(()=>{});
}
