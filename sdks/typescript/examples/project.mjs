// Apache-2.0. Inspect a saved project; approval is explicit and limited.
import { Client } from '../dist/esm/index.js';
const client = new Client({baseURL:process.env.DM_API_BASE_URL,apiKey:process.env.DOCMATCH_API_KEY});
const project = client.project(process.argv[2]);
const saved = await project.get();
const analysis = await project.estimate(saved.revision);
if (analysis.run) console.log('Conciliação existente:',analysis.run.id,analysis.run.status);
else if (analysis.quote) {
 const quote=analysis.quote;
 console.log(`${saved.name} · limite: ${quote.cap_credits} créditos`);
 const approved=Number(process.env.DOCMATCH_APPROVE_CAP ?? 0);
 if (approved > 0 && approved >= quote.cap_credits) {
  const result=await project.reconcile(saved.revision,quote);
  console.log('Concluída:',(await project.wait(result.run.id)).id);
 } else console.log('Para aprovar, defina DOCMATCH_APPROVE_CAP com o limite aceito e execute novamente.');
} else console.log('Resultado indisponível; confira o projeto no app.');
