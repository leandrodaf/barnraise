<script lang="ts">
	import type { ProcSummary } from '$lib/gen/barnraise/v1/barnraise_pb';
	import { t } from '$lib/i18n';
	import { bytes, dur, num, pct } from '$lib/format';
	import { BG, SOFT } from '$lib/status';
	import { kindTone } from '$lib/procs.svelte';

	// What runs somewhere, in one line: processes, processor, memory, and a bar
	// of the kinds of process (agents, tests, browsers…).
	let { p, cores = 0, compact: small = false }: { p?: ProcSummary; cores?: number; compact?: boolean } = $props();
	const kinds = $derived(Object.entries(p?.byKind || {}).filter(([, n]) => n > 0).sort((a, b) => b[1] - a[1]));
	const total = $derived(kinds.reduce((s, [, n]) => s + n, 0) || 1);
</script>

{#if !p || !p.count}
	<p class="text-xs text-muted-foreground">{t('procNone')}</p>
{:else}
	<div class="space-y-2">
		<div class="num grid {small ? 'grid-cols-3' : 'grid-cols-2 sm:grid-cols-4'} gap-2 text-[11px]">
			<div class="rounded-md bg-muted/50 px-2 py-1.5"><p class="text-muted-foreground">{t('procCount')}</p><p class="text-sm font-semibold">{p.count}</p></div>
			<div class="rounded-md bg-muted/50 px-2 py-1.5" title={cores ? t('procOfCores', pct(p.cpuPct / cores)) : ''}><p class="text-muted-foreground">CPU</p><p class="text-sm font-semibold">{pct(p.cpuPct)}</p></div>
			<div class="rounded-md bg-muted/50 px-2 py-1.5"><p class="text-muted-foreground">RAM</p><p class="text-sm font-semibold">{bytes(p.rssBytes)}</p></div>
			{#if !small}<div class="rounded-md bg-muted/50 px-2 py-1.5" title={t('procTotalsHelp')}><p class="text-muted-foreground">{t('procCpuTime')}</p><p class="text-sm font-semibold">{dur(p.cpuSeconds)}</p><p class="text-muted-foreground">{t('procPeak', bytes(p.peakRssBytes))}</p></div>{/if}
		</div>
		<div class="flex h-1.5 overflow-hidden rounded-full bg-muted" aria-hidden="true">
			{#each kinds as [k, n] (k)}<span class="h-full {BG[kindTone(k)]}" style="width:{(n * 100) / total}%"></span>{/each}
		</div>
		<div class="flex flex-wrap gap-1">
			{#each kinds as [k, n] (k)}<span class="num rounded px-1.5 py-0.5 text-[10px] {SOFT[kindTone(k)]}">{t('pk_' + k)} {num(n)}</span>{/each}
		</div>
	</div>
{/if}
