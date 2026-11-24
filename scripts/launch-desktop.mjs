import { spawn } from 'node:child_process';
import { createRequire } from 'node:module';
import { resolve } from 'node:path';
import { buildApp } from './build-app.mjs';
import { buildCore, root } from './build-core.mjs';

const require = createRequire(import.meta.url);
const core = buildCore();
await buildApp();
const child = spawn(require('electron'), [root], {
  cwd:root, stdio:'inherit', shell:false,
  env:{...process.env, RELAYDOCK_CORE:core, RELAYDOCK_RESOURCES:resolve(root,'resources')},
});
child.on('error', (err)=>{console.error(err.message);process.exitCode=1;});
child.on('exit',(code)=>{process.exitCode=code??1;});
process.once('SIGINT',()=>child.kill());
