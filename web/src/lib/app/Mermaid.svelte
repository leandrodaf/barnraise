<script lang="ts">
	import * as Dialog from '$lib/components/ui/dialog';
	import { Button } from '$lib/components/ui/button';
	import Maximize from '@lucide/svelte/icons/maximize-2';
	import CodeIcon from '@lucide/svelte/icons/code-xml';
	import ImageIcon from '@lucide/svelte/icons/workflow';
	import CopyIcon from '@lucide/svelte/icons/copy';
	import TriangleIcon from '@lucide/svelte/icons/triangle-alert';
	import { toast } from 'svelte-sonner';
	import { t } from '$lib/i18n';
	import { theme } from '$lib/theme.svelte';
	import { renderMermaid, kindOf } from './mermaid';

	// A Mermaid diagram in the panel's look: drawn on a blueprint grid, with its
	// source a click away. The SVG comes from Mermaid in strict mode and is
	// shown as an image, so nothing in it reaches the page.
	let { source, title = '', compact = false, tone = '' }: { source: string; title?: string; compact?: boolean; tone?: '' | 'before' | 'after' } = $props();

	let svg = $state(''); // a blob: URL of the drawn SVG, shown as an image
	let size = $state({ w: 0, h: 0 });
	let error = $state('');
	let showCode = $state(false);
	let big = $state(false);
	const kind = $derived(kindOf(source));
	const accent = $derived(tone === 'after' ? 'var(--st-done)' : tone === 'before' ? 'var(--st-idle)' : 'var(--st-work)');

	$effect(() => {
		const src = source;
		void theme.current;
		let live = true;
		let url = '';
		renderMermaid(src, tone)
			.then((s) => {
				if (!live) return;
				// An image never runs what the SVG holds and never reaches the page.
				url = URL.createObjectURL(new Blob([s], { type: 'image/svg+xml' }));
				const vb = s.match(/viewBox="[\d.-]+ [\d.-]+ ([\d.]+) ([\d.]+)"/);
				size = vb ? { w: Number(vb[1]), h: Number(vb[2]) } : { w: 0, h: 0 };
				svg = url;
				error = '';
			})
			.catch((e) => live && ((svg = ''), (error = String(e?.message || e).split('\n')[0])));
		return () => {
			live = false;
			if (url) URL.revokeObjectURL(url);
		};
	});

	async function copy() {
		try {
			await navigator.clipboard.writeText('```mermaid\n' + source + '\n```');
			toast.success(t('docCopied'));
		} catch {
			/* clipboard refused */
		}
	}
</script>

<figure aria-label={title || t('docDiagram')} style="--acc: {accent}"
	class="dg group relative min-w-0 overflow-hidden rounded-xl border bg-card transition-shadow {tone === 'after' ? 'dg-after' : ''}">
	<!-- the accent line on top: the side's color fading out -->
	<div class="pointer-events-none absolute inset-x-0 top-0 h-px" style="background: linear-gradient(90deg, transparent, var(--acc) 30%, var(--acc) 70%, transparent)"></div>
	<div class="relative flex items-center justify-between gap-2 border-b bg-muted/30 px-3 py-1.5 backdrop-blur">
		<span class="flex min-w-0 items-center gap-2 truncate font-mono text-[11px] font-medium tracking-wider uppercase" style="color: {tone ? 'var(--acc)' : ''}">
			<span class="relative flex size-2 shrink-0">
				{#if tone === 'after'}<span class="absolute inline-flex size-full animate-ping rounded-full opacity-60" style="background: var(--acc)"></span>{/if}
				<span class="relative inline-flex size-2 rounded-full" style="background: var(--acc)"></span>
			</span>
			<span class="truncate">{title || t('docDiagram')}</span>
			{#if kind}<span class="rounded border px-1 py-px text-[9px] tracking-widest text-muted-foreground">{kind}</span>{/if}
		</span>
		<div class="flex shrink-0 items-center gap-0.5 opacity-60 transition-opacity group-hover:opacity-100">
			<Button variant="ghost" size="icon-sm" title={showCode ? t('docShowDiagram') : t('docShowCode')} onclick={() => (showCode = !showCode)}>
				{#if showCode}<ImageIcon class="size-3.5" />{:else}<CodeIcon class="size-3.5" />{/if}
			</Button>
			<Button variant="ghost" size="icon-sm" title={t('docCopySource')} onclick={copy}><CopyIcon class="size-3.5" /></Button>
			{#if svg && !showCode}<Button variant="ghost" size="icon-sm" title={t('docExpand')} onclick={() => (big = true)}><Maximize class="size-3.5" /></Button>{/if}
		</div>
	</div>
	{#if showCode}
		<pre class="num max-h-96 overflow-auto bg-muted/20 p-4 text-xs leading-relaxed">{source}</pre>
	{:else if error}
		<div class="space-y-2 p-3">
			<p class="flex items-start gap-1.5 text-xs text-st-needs"><TriangleIcon class="mt-0.5 size-3.5 shrink-0" />{t('docDiagramError', error)}</p>
			<pre class="num max-h-64 overflow-auto rounded bg-muted/60 p-2 text-xs">{source}</pre>
		</div>
	{:else if svg}
		<button type="button" class="grid-bg relative block w-full cursor-zoom-in overflow-auto p-5 {compact ? 'max-h-72' : 'max-h-[30rem]'}" onclick={() => (big = true)} aria-label={t('docExpand')}>
			<span class="corner tl"></span><span class="corner tr"></span><span class="corner bl"></span><span class="corner br"></span>
			<img src={svg} alt={title || t('docDiagram')} width={size.w || undefined} height={size.h || undefined} class="dg-img relative mx-auto h-auto max-w-full" />
		</button>
	{:else}
		<div class="grid-bg relative h-40 overflow-hidden"><div class="scan absolute inset-y-0 w-1/3"></div></div>
	{/if}
</figure>

<Dialog.Root bind:open={big}>
	<Dialog.Content class="max-h-[92vh] w-[96vw] max-w-[96vw]! overflow-auto sm:max-w-[96vw]">
		<Dialog.Header><Dialog.Title>{title || t('docDiagram')}</Dialog.Title></Dialog.Header>
		<div class="grid-bg overflow-auto rounded-lg border p-6" style="--acc: {accent}">
			{#if svg}<img src={svg} alt={title || t('docDiagram')} class="mx-auto h-auto" style="min-width: min(100%, {Math.max(size.w * 1.5, 640)}px)" />{/if}
		</div>
	</Dialog.Content>
</Dialog.Root>

<style>
	/* A blueprint: a faint dot grid in the foreground color, denser near the center. */
	.grid-bg {
		background-color: color-mix(in oklch, var(--card) 100%, transparent);
		background-image:
			radial-gradient(circle at 50% 40%, color-mix(in oklch, var(--acc) 7%, transparent), transparent 70%),
			radial-gradient(color-mix(in oklch, var(--foreground) 13%, transparent) 1px, transparent 1.2px);
		background-size: 100% 100%, 14px 14px;
		background-position: 0 0, 7px 7px;
	}
	.dg-after {
		border-color: color-mix(in oklch, var(--acc) 35%, var(--border));
		box-shadow: 0 0 0 1px color-mix(in oklch, var(--acc) 12%, transparent), 0 10px 40px -18px color-mix(in oklch, var(--acc) 55%, transparent);
	}
	.dg:hover { box-shadow: 0 0 0 1px color-mix(in oklch, var(--acc) 25%, transparent), 0 12px 40px -20px color-mix(in oklch, var(--acc) 60%, transparent); }
	.dg-img { animation: rise 0.45s cubic-bezier(0.2, 0.7, 0.2, 1) both; }
	@keyframes rise { from { opacity: 0; transform: translateY(6px) scale(0.99); filter: blur(2px); } }
	/* Corner ticks, like a frame on a drawing board. */
	.corner { position: absolute; width: 10px; height: 10px; border-color: color-mix(in oklch, var(--acc) 70%, transparent); pointer-events: none; }
	.tl { top: 8px; left: 8px; border-top: 1px solid; border-left: 1px solid; }
	.tr { top: 8px; right: 8px; border-top: 1px solid; border-right: 1px solid; }
	.bl { bottom: 8px; left: 8px; border-bottom: 1px solid; border-left: 1px solid; }
	.br { bottom: 8px; right: 8px; border-bottom: 1px solid; border-right: 1px solid; }
	/* While drawing: a scan line crosses the grid. */
	.scan { background: linear-gradient(90deg, transparent, color-mix(in oklch, var(--acc) 18%, transparent), transparent); animation: scan 1.3s ease-in-out infinite; }
	@keyframes scan { from { left: -33%; } to { left: 100%; } }
	@media (prefers-reduced-motion: reduce) { .dg-img, .scan { animation: none; } }
</style>
