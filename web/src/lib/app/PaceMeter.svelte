<script lang="ts">
	import type { UsageWindow } from '$lib/gen/barnraise/v1/barnraise_pb';
	import { t } from '$lib/i18n';
	import { dur, pct } from '$lib/format';

	// One usage window as the daemon's outlook says it (domain.AccountOutlook):
	// used, the pace mark, the reset and, ahead of the pace, when it runs out.
	let { label, w, limited = false, ceiling = 0 }: { label: string; w?: UsageWindow; limited?: boolean; ceiling?: number } = $props();
	const used = $derived(w?.usedPct || 0);
	const tone = $derived(limited && used >= 100 ? 'bg-st-fail' : w?.runsOutIn ? 'bg-st-needs' : 'bg-st-done');
</script>

<div class="min-w-0 space-y-1.5">
	<div class="flex items-baseline justify-between gap-2 text-sm">
		<span class="min-w-0 truncate text-muted-foreground" title={label}>{label}</span>
		<span class="num font-medium">{pct(used)}</span>
	</div>
	<div class="relative h-2 rounded-full bg-muted" role="progressbar" aria-label={label} aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(used)}>
		<div class="h-2 rounded-full transition-all {tone}" style="width:{used}%"></div>
		{#if w?.pacePct}<div class="absolute -top-1 h-4 w-0.5 rounded bg-foreground/70" style="left:{w.pacePct}%" title={t('paceMark')}></div>{/if}
		{#if ceiling}<div class="absolute top-0 h-2 w-px bg-st-fail" style="left:{Math.min(100, ceiling)}%" title={t('ceiling', ceiling)}></div>{/if}
	</div>
	<p class="num text-[11px] {w?.runsOutIn ? 'text-st-needs' : 'text-muted-foreground'}">
		{#if w?.runsOutIn}{t('runsOut', dur(w.runsOutIn))}{:else if w?.resetsIn}{t('freeUntil', pct(100 - used), dur(w.resetsIn))}{/if}
	</p>
</div>
