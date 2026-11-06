// Records real local UI + network. Only the public synthetic fixture is accepted.
import { chromium, expect } from '../../apps/web/node_modules/@playwright/test/index.mjs';
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs';
import { resolve, join } from 'node:path';
const site=process.env.DM_DEMO_SITE??'http://localhost:14321',api=process.env.DM_DEMO_API??'http://localhost:18080';
if(!['localhost','127.0.0.1'].includes(new URL(api).hostname))throw Error('Recording requires a local development API');
const out=resolve('scratch/master-plan-demo');mkdirSync(out,{recursive:true});
const strings=JSON.parse(readFileSync('packages/locales/pt-BR.json','utf8'));
const browser=await chromium.launch({channel:'chrome',headless:true});
const ctx=await browser.newContext({viewport:{width:1280,height:800},locale:'pt-BR',colorScheme:'light',recordVideo:{dir:join(out,'web-video'),size:{width:1280,height:800}},acceptDownloads:true});
const page=await ctx.newPage();const errors=[];page.on('pageerror',e=>errors.push(e.message));page.on('dialog',d=>{errors.push('Unstyled dialog: '+d.type());void d.dismiss()});
try{
 const login=await ctx.request.post(api+'/v1/auth/dev/login',{data:{},headers:{Origin:site}});expect(login.ok()).toBeTruthy();
 await page.goto(site);await page.waitForTimeout(1400);
 await page.getByText('R$ 100',{exact:false}).first().scrollIntoViewIfNeeded().catch(()=>{});await page.waitForTimeout(1500);
 await page.goto(site+'/app');
 const panel=page.locator('details').filter({has:page.getByText(strings.workspace.title,{exact:true})});await page.getByRole('button',{name:strings.workspace.open,exact:true}).click();
 await panel.getByRole('button',{name:strings.workspace.example_label,exact:true}).click();await panel.getByRole('button',{name:strings.workspace.prepare,exact:true}).click();
 await expect(panel.getByRole('button',{name:strings.workspace.approve,exact:true})).toBeVisible({timeout:65000});
 const preview=page.frameLocator('iframe[title="'+strings.workspace.preview+'"]');await expect(preview.getByRole('heading',{name:'Pendências',exact:true})).toBeVisible();
 const frame=page.frames().find(f=>f.url()==='about:srcdoc');expect(await frame.evaluate(()=>getComputedStyle(document.body).backgroundColor)).toBe('rgb(252, 252, 253)');
 await page.waitForTimeout(1800);await panel.getByRole('button',{name:strings.workspace.approve,exact:true}).click();await expect(panel.getByText(strings.workspace.applied,{exact:true})).toBeVisible();await page.getByRole('dialog').getByRole('button',{name:strings.common.close,exact:true}).click();
 const pack=resolve('testdata/golden/vantara-twin');const names=['01_contas_a_receber_protheus_jul2026.csv','02_titulos_unidade_norte_omie_jul2026.csv','03_extrato_bancario_jul2026.csv','04_caixa_de_entrada_contas_a_pagar.txt'];
 await page.locator('input[type="file"]').setInputFiles(names.map(n=>join(pack,n)));await page.waitForTimeout(1400);
 await page.getByRole('button',{name:strings.form.submit,exact:true}).click();await page.getByRole('button',{name:strings.app.confirm_start,exact:true}).click({timeout:120000});
 await page.waitForURL(/\/resultados\?run=/,{timeout:180000});await expect(page.getByRole('button',{name:strings.results.download_corrected,exact:true})).toBeVisible({timeout:180000});
 const id=new URL(page.url()).searchParams.get('run');const run=await (await ctx.request.get(api+'/v1/runs/'+id)).json();const findings=await (await ctx.request.get(api+'/v1/runs/'+id+'/findings?limit=100')).json();writeFileSync(join(out,'website-run.json'),JSON.stringify({run,findings:findings.items},null,2));
 await page.waitForTimeout(2000);const download=page.waitForEvent('download');await page.getByRole('button',{name:strings.results.download_corrected,exact:true}).click();await (await download).saveAs(join(out,'website-corrected.xlsx'));
 await page.getByText(strings.results.all_heading,{exact:true}).click();await expect(page.getByTestId('finding-row')).toHaveCount(23);await page.getByRole('heading',{name:'Pendências',exact:true}).scrollIntoViewIfNeeded();await page.waitForTimeout(2200);
 await page.getByTestId('finding-row').first().getByRole('button').click();await expect(page.getByRole('dialog')).toBeVisible();await page.waitForTimeout(2200);await page.getByRole('dialog').getByRole('button',{name:strings.results.drawer_close,exact:true}).click();
 await page.screenshot({path:join(out,'website-final.png'),fullPage:false});
 if(errors.length)throw Error(errors.join('\n'));
 writeFileSync(join(out,'web-ui.json'),JSON.stringify({passed:true,run_id:id,preview_styles:true,unstyled_dialogs:0,download:true,findings:23},null,2));
 console.log('Website recording completed:',id);
}finally{
 await ctx.request.post(api+'/v1/workspace/rollback',{data:{version:1},headers:{Origin:site}}).catch(()=>{});
 await ctx.close();await browser.close();
}
