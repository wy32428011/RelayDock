import { fileURLToPath } from 'node:url';
import react from '@vitejs/plugin-react';
import { defineConfig } from 'vite';

const csp = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; base-uri 'none'; form-action 'none'; connect-src 'none'; frame-src 'none'; object-src 'none'";

export default defineConfig({
  root: fileURLToPath(new URL('./', import.meta.url)),
  base: './',
  plugins: [react(), {
    name: 'relaydock-local-csp',
    apply: 'build',
    transformIndexHtml: () => [{ tag: 'meta', attrs: { 'http-equiv': 'Content-Security-Policy', content: csp }, injectTo: 'head-prepend' }],
  }],
  build: {outDir: 'dist/renderer', emptyOutDir: true, target: 'es2023'},
});
