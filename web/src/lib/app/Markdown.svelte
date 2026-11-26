<script lang="ts">
	import SquareIcon from '@lucide/svelte/icons/square';
	import SquareCheckIcon from '@lucide/svelte/icons/square-check';
	import { parse, type Inline } from './markdown';
	import Mermaid from './Mermaid.svelte';
	import { href } from '$lib/router.svelte';

	let { text }: { text: string } = $props();
	// Tracker text arrives wrapped in an untrusted marker; it stays out of view.
	const blocks = $derived(parse(text.replace(/^<<<UNTRUSTED[^\n]*\n?/m, '').replace(/\n?>>>\s*$/m, '')));
	// The heading above a diagram names it ("Before", "After").
	const lastHeading = (i: number) => {
		for (let j = i - 1; j >= 0; j--) {
			const b = blocks[j];
			if (b.k === 'h') return b.parts.map((p) => p.v).join('');
			if (j === i - 1 && b.k === 'p' && b.parts.length === 1 && b.parts[0].t === 'bold') return b.parts[0].v;
			if (b.k === 'pre') return '';
		}
		return '';
	};
	const H = ['', 'text-base', 'text-base', 'text-sm', 'text-sm', 'text-sm', 'text-sm'];
</script>

{#snippet parts(ps: Inline[])}{#each ps as p, i (i)}{#if p.t === 'bold'}<strong class="font-semibold">{p.v}</strong>{:else if p.t === 'code'}<code class="num rounded bg-muted px-1 py-0.5 text-[0.85em]">{p.v}</code>{:else if p.t === 'em'}<em>{p.v}</em>{:else if p.t === 'item'}<a href={p.id?.startsWith('I-') ? href('item', p.project || '', p.id || '') : p.id?.startsWith('P-') ? href('proposals') : href('home')} class="num inline-flex items-center rounded-md border border-st-work/25 bg-st-work/10 px-1.5 py-px text-[0.85em] font-medium text-st-work no-underline hover:bg-st-work/20" title={p.project}><span class="opacity-60">{p.project}/</span>{p.id}</a>{:else if p.t === 'link'}<a href={p.href} target="_blank" rel="noreferrer" class="font-medium text-st-work underline decoration-st-work/30 underline-offset-2 [overflow-wrap:anywhere] hover:decoration-st-work">{p.v}</a>{:else}{p.v}{/if}{/each}{/snippet}

<div class="space-y-2.5 text-sm leading-relaxed">
	{#each blocks as b, i (i)}
		{#if b.k === 'h'}<p class="pt-2 font-semibold tracking-tight first:pt-0 {H[b.level]}">{@render parts(b.parts)}</p>
		{:else if b.k === 'p'}<p class="text-foreground/90">{@render parts(b.parts)}</p>
		{:else if b.k === 'li'}
			<div class="flex gap-2" style="padding-left:{b.depth * 1.25}rem">
				{#if b.check}{#if b.done}<SquareCheckIcon class="mt-0.5 size-4 shrink-0 text-st-done" />{:else}<SquareIcon class="mt-0.5 size-4 shrink-0 text-muted-foreground" />{/if}
				{:else if b.ordered}<span class="num mt-px w-4 shrink-0 text-right text-xs text-muted-foreground">{b.ordered}.</span>
				{:else}<span class="mt-2 size-1 shrink-0 rounded-full bg-muted-foreground"></span>{/if}
				<span class="min-w-0">{@render parts(b.parts)}</span>
			</div>
		{:else if b.k === 'quote'}<p class="border-l-2 pl-3 text-muted-foreground">{@render parts(b.parts)}</p>
		{:else if b.k === 'pre' && b.lang === 'mermaid'}{@const ttl = lastHeading(i)}<Mermaid source={b.v} title={ttl} tone={/\b(after|depois)\b/i.test(ttl) ? 'after' : /\b(before|antes)\b/i.test(ttl) ? 'before' : ''} />
		{:else if b.k === 'table'}
			<div class="overflow-x-auto rounded-lg border">
				<table class="w-full text-xs">
					<thead class="bg-muted/50"><tr>{#each b.head as c, j (j)}<th class="px-3 py-2 font-semibold whitespace-nowrap" style="text-align:{b.align[j] || 'left'}">{@render parts(c)}</th>{/each}</tr></thead>
					<tbody>{#each b.rows as r, ri (ri)}<tr class="border-t">{#each r as c, j (j)}<td class="px-3 py-1.5 align-top" style="text-align:{b.align[j] || 'left'}">{@render parts(c)}</td>{/each}</tr>{/each}</tbody>
				</table>
			</div>
		{:else if b.k === 'pre'}<pre class="num overflow-x-auto rounded-lg bg-muted/60 p-3 text-xs">{b.v}</pre>
		{:else}<hr class="border-border" />{/if}
	{/each}
</div>
