import { fileURLToPath, pathToFileURL } from 'node:url';
import { resolve } from 'node:path';
import { build as bundle } from 'esbuild';
import { build as vite } from 'vite';

const desktop = fileURLToPath(new URL('../apps/desktop/', import.meta.url));

export async function buildApp() {
  await vite({configFile:resolve(desktop,'vite.config.ts'), mode:'production'});
  await Promise.all(['main','preload'].map(layer=>bundle({
    entryPoints:[resolve(desktop,'src',layer,'index.ts')],
    outfile:resolve(desktop,'dist',`${layer}.cjs`),
    bundle:true, format:'cjs', platform:'node', target:'node22', external:['electron'],
    minify:true, legalComments:'linked', logLevel:'info',
  })));
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  await buildApp();
}
