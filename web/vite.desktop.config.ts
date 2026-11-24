import { svelte } from '@sveltejs/vite-plugin-svelte';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig } from 'vite';
import path from 'node:path';

// The desktop app's launcher: a page of its own, built into the Wails module.
export default defineConfig({
	root: 'desktop',
	plugins: [tailwindcss(), svelte()],
	base: './',
	resolve: { alias: { $lib: path.resolve('./src/lib') } },
	build: { outDir: '../../desktop/frontend/dist', emptyOutDir: true, assetsInlineLimit: 0 },
	server: { port: 5174, strictPort: true }
});
