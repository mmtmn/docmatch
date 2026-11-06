// This example approves the returned quote. Run only when you accept its cap.
import {readFile,writeFile} from 'node:fs/promises';import {basename,extname} from 'node:path';
import {Client} from '../dist/esm/index.js';
const c=new Client({baseURL:process.env.DM_API_BASE_URL,apiKey:process.env.DOCMATCH_API_KEY});const ids=[];
for(const path of process.argv.slice(2)){const file=await c.upload(basename(path),await readFile(path),extname(path)==='.txt'?'text/plain':'text/csv');ids.push(file.id)}
const input=c.input(ids),quote=await c.estimate(input);console.error(`Approved example run: cap ${quote.cap_credits} tokens`);
const started=await c.run(quote,input),run=await c.wait(started.id),findings=await c.findings(run.id);
const data=await c.export(run.id,'xlsx');if(process.env.DOCMATCH_EXPORT_PATH)await writeFile(process.env.DOCMATCH_EXPORT_PATH,data,{mode:0o600});
console.log(JSON.stringify({run,findings}));
