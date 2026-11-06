import { execFileSync } from 'node:child_process';
import { mkdirSync, writeFileSync } from 'node:fs';
execFileSync('tsc',['-p','tsconfig.json'],{stdio:'inherit'});
execFileSync('tsc',['-p','tsconfig.json','--module','commonjs','--moduleResolution','node','--outDir','dist/cjs'],{stdio:'inherit'});
mkdirSync('dist/cjs',{recursive:true});writeFileSync('dist/cjs/package.json','{"type":"commonjs"}\n');
