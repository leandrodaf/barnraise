<script lang="ts">
	import { money, compact } from '$lib/format';
	// Spend per day as columns, newest on the right, with the value on hover.
	let { days }: { days: [string, { costUsd: number; tokens: bigint }][] } = $props();
	const rows = $derived(days.slice(-30));
	const max = $derived(Math.max(0.01, ...rows.map(([, u]) => u.costUsd)));
</script>

{#if rows.length}
	<div class="flex h-36 items-end gap-1">
		{#each rows as [d, u] (d)}
			<div class="group relative flex h-full flex-1 flex-col justify-end">
				<div class="w-full rounded-t bg-st-work/70 transition-colors group-hover:bg-st-work" style="height:{Math.max(2, (u.costUsd / max) * 100)}%"></div>
				<div class="num pointer-events-none absolute bottom-full left-1/2 z-10 mb-1 hidden -translate-x-1/2 rounded-md border bg-popover px-2 py-1 text-[11px] whitespace-nowrap shadow group-hover:block">{d} · {money(u.costUsd)} · {compact(u.tokens)}</div>
			</div>
		{/each}
	</div>
	<div class="num mt-1 flex justify-between text-[10px] text-muted-foreground"><span>{rows[0][0]}</span><span>{rows[rows.length - 1][0]}</span></div>
{/if}
