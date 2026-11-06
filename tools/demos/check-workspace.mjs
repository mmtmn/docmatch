// Real local customization/accessibility check. Uses a separate dev account and restores its version.
import { chromium, expect } from '../../apps/web/node_modules/@playwright/test/index.mjs';
import { AxeBuilder } from '../../apps/web/node_modules/@axe-core/playwright/dist/index.mjs';
import { readFile, writeFile } from 'node:fs/promises';
import assert from 'node:assert/strict';
const origin=process.env.DM_DEMO_SITE??'http://localhost:14321', api=process.env.DM_DEMO_API??'http://localhost:18080';
if(!['localhost','127.0.0.1'].includes(new URL(api).hostname)) throw Error('Use a local development API');
const strings=JSON.parse(await readFile('packages/locales/pt-BR.json','utf8'));
const browser=await chromium.launch({channel:'chrome',headless:true});
const context=await browser.newContext({locale:'pt-BR',viewport:{width:1280,height:900},reducedMotion:'reduce'});
const page=await context.newPage(), errors=[], checks=[];
page.on('pageerror',e=>errors.push(e.message)); page.on('dialog',d=>{errors.push('native-dialog');void d.dismiss()});
let version;
try {
 expect((await context.request.post(api+'/v1/auth/dev/login',{data:{email:'workspace-ui-check@docmatch.test'},headers:{Origin:origin}})).ok()).toBeTruthy();
 const workspace=await (await context.request.get(api+'/v1/workspace')).json();version=workspace.version||1;
 await page.goto(origin+'/app');
 for(const size of [{width:1280,height:900},{width:768,height:1024},{width:390,height:844}]) {
  await page.setViewportSize(size);
  const zone=await page.getByLabel(strings.dropzone.label,{exact:true}).boundingBox();
  assert.ok(zone && zone.height>100,'drop zone stays usable');
  const submit=await page.getByRole('button',{name:strings.form.submit,exact:true}).boundingBox();
  assert.ok(submit && submit.y+submit.height<=size.height,'submit visible');
  assert.equal(await page.evaluate(()=>document.documentElement.scrollHeight>innerHeight),false,'first screen fits');
 }
 await page.setViewportSize({width:1280,height:900});
 const panel=page.locator('details').filter({has:page.getByText(strings.workspace.title,{exact:true})});
 await page.getByRole('button',{name:strings.workspace.open,exact:true}).click();
 await panel.getByRole('button',{name:strings.workspace.example_dark,exact:true}).click();
 await panel.getByRole('button',{name:strings.workspace.prepare,exact:true}).click();
 await expect(panel.getByRole('button',{name:strings.workspace.approve,exact:true})).toBeVisible();
 const frames=()=>page.frames().find(f=>f.url()==='about:srcdoc');
 assert.equal(await frames().evaluate(()=>document.scripts.length),0);
 // Analyze the exact script-free preview markup separately; production sandbox stays intact.
 const preview=await context.newPage();
 const markup=await page.locator('iframe').getAttribute('srcdoc');
 const css=await readFile('apps/web/public/workspace-preview.css','utf8');
 await preview.setContent(markup.replace(/<meta http-equiv="Content-Security-Policy"[^>]*>/,'').replace(/<link rel="stylesheet"[^>]*>/, '<style>'+css+'</style>'));
 for(const width of [1280,768,390])for(const theme of ['light','dark']){
  await page.setViewportSize({width,height:900});await page.emulateMedia({colorScheme:theme});
  const scan=await new AxeBuilder({page}).exclude('iframe').withTags(['wcag2a','wcag2aa','wcag21aa','wcag22aa']).analyze();
  assert.deepEqual(scan.violations.map(v=>({id:v.id,nodes:v.nodes.map(n=>n.target)})),[],width+' '+theme);
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
  await preview.setViewportSize({width:Math.max(240,width-80),height:320});
  await preview.emulateMedia({colorScheme:theme});
  const previewScan=await new AxeBuilder({page:preview}).withTags(['wcag2a','wcag2aa','wcag21aa','wcag22aa']).analyze();
  assert.deepEqual(previewScan.violations.map(v=>v.id),[],'preview '+width+' '+theme);
  assert.equal(await preview.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
  checks.push({width,theme,violations:0,preview_violations:0,overflow:false});
 }
 await panel.getByRole('button',{name:strings.workspace.approve,exact:true}).click();
 await expect(panel.getByText(strings.workspace.applied,{exact:true})).toBeVisible();
 await expect(page.locator('[data-workspace-mode="dark"]')).toBeVisible();
 await page.getByRole('dialog').getByRole('button',{name:strings.common.close,exact:true}).click();
 await page.reload();await expect(page.locator('[data-workspace-mode="dark"]')).toBeVisible();
 const disposable=await context.request.post(api+'/v1/auth/email/start',{data:{email:'public-demo@mailinator.com'},headers:{Origin:origin}});
 expect(disposable.status()).toBe(422);expect((await disposable.json()).code).toBe('disposable_email');
 assert.deepEqual(errors,[]);await writeFile('scratch/master-plan-demo/workspace-ui-checks.json',JSON.stringify({checks,iframe_scripts:0,apply_reload:true,disposable_email:422,native_dialogs:0},null,2));
 console.log('Workspace: six axe checks, no overflow/dialogs, approval persisted, disposable email refused.');
}finally{
await page.screenshot({path:'scratch/master-plan-demo/workspace-check.png',fullPage:true});
 if(version)await context.request.post(api+'/v1/workspace/rollback',{data:{version},headers:{Origin:origin}});
 await browser.close();
}
