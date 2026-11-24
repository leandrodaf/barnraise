<script lang="ts">
	import type { Snippet } from 'svelte';
	// A panel: title, an optional count and a right-side slot, then the body.
	let { title, count, sub = '', right, children, pad = true, class: cls = '', tone = '' }:
		{ title: string; count?: number | string; sub?: string; right?: Snippet; children: Snippet; pad?: boolean; class?: string; tone?: string } = $props();
</script>

<section class="min-w-0 overflow-hidden rounded-xl border bg-card {tone} {cls}">
	<header class="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-b px-4 py-3">
		<div class="min-w-0">
			<h2 class="flex items-center gap-2 text-sm font-semibold">{title}{#if count !== undefined}<span class="num text-xs font-normal text-muted-foreground">{count}</span>{/if}</h2>
			{#if sub}<p class="truncate text-xs text-muted-foreground">{sub}</p>{/if}
		</div>
		{#if right}<div class="min-w-0">{@render right()}</div>{/if}
	</header>
	<div class={pad ? 'p-4' : ''}>{@render children()}</div>
</section>
