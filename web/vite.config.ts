import { svelte } from '@sveltejs/vite-plugin-svelte';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig } from 'vite';
import path from 'node:path';

// The panel is served by the daemon at / and embedded in the desktop app, so
// the build goes straight into the Go package that embeds it, with relative
// paths and every asset in its own file (no inline script: the CSP forbids it).
// `pnpm dev` talks to the local daemon through the proxy below.
const daemon = process.env.BARNRAISE_URL || 'http://127.0.0.1:7420';

export default defineConfig({
	plugins: [tailwindcss(), svelte()],
	base: './',
	resolve: { alias: { $lib: path.resolve('./src/lib') } },
	build: {
		outDir: '../internal/adapters/in/panel/dist',
		emptyOutDir: true,
		assetsInlineLimit: 0,
		modulePreload: { polyfill: false },
		sourcemap: false,
		chunkSizeWarningLimit: 900
	},
	server: {
		proxy: {
			'/barnraise.v1.BarnraiseService': daemon,
			'/v1': daemon,
			'/healthz': daemon
		}
	}
});
