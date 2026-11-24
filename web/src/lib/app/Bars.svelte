<script lang="ts">
		import { pct } from '$lib/format';
	import Empty from './Empty.svelte';

	let {
		map,
		fmt,
		share = false,
		limit = 12,
		link,
		name
	}: { map: Record<string, number | bigint> | undefined; fmt: (v: number) => string; share?: boolean; limit?: number; link?: (k: string) => string; name?: (k: string) => string } = $props();

	const rows = $derived(
		Object.entries(map || {})
			.map(([k, v]) => [k, Number(v) || 0] as [string, number])
			.filter(([, v]) => v > 0)
			.sort((a, b) => b[1] - a[1])
			.slice(0, limit)
	);
	const max = $derived(rows[0]?.[1] || 1);
	const total = $derived(rows.reduce((s, [, v]) => s + v, 0) || 1);
</script>

{#if !rows.length}
	<Empty />
{:else}
	<ul class="space-y-2">
		{#each rows as [k, v] (k)}
			<li class="grid grid-cols-[minmax(0,7rem)_1fr_auto] items-center gap-3 text-sm sm:grid-cols-[minmax(0,9rem)_1fr_auto]">
				{#if link}<a class="num truncate text-xs hover:underline" href={link(k)} title={name ? name(k) : k}>{name ? name(k) : k}</a>{:else}<span class="truncate" title={k}>{name ? name(k) : k}</span>{/if}
				<span class="h-1.5 rounded-full bg-muted"><span class="block h-full rounded-full {share && v / total > 0.3 ? 'bg-st-work' : 'bg-st-work/60'}" style="width: {(v * 100) / max}%"></span></span>
				<span class="num text-xs">{fmt(v)}{#if share}<span class="ml-1 text-xs text-muted-foreground">{pct((v * 100) / total)}</span>{/if}</span>
			</li>
		{/each}
	</ul>
{/if}
