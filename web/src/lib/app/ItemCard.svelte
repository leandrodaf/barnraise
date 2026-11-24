<script lang="ts">
	import GitBranchIcon from '@lucide/svelte/icons/git-branch';
	import ServerIcon from '@lucide/svelte/icons/server';
	import { t } from '$lib/i18n';
	import { compact, money, since } from '$lib/format';
	import { TEXT, BG, toneOfItem, signalTone, signalText } from '$lib/status';
	import type { Row } from './items';
	import { session } from '$lib/session.svelte';
	import { openItem } from './open';
	import StatusIcon from './StatusIcon.svelte';

	let { r, compact: dense = false }: { r: Row; compact?: boolean } = $props();
	const tone = $derived(toneOfItem(r));
	const sub = $derived({ text: signalText(r), tone: signalTone(r) });
	const prTone = (st: string) => (st === 'merged' ? 'text-st-review' : st === 'closed' ? 'text-st-fail' : st === 'draft' ? 'text-muted-foreground' : 'text-st-done');
</script>

<button class="group w-full rounded-lg border bg-card text-left transition-all hover:-translate-y-px hover:border-foreground/20 hover:shadow-sm" onclick={() => openItem(r.project, r.id)}>
	<div class="flex items-start gap-2.5 {dense ? 'p-2.5' : 'p-3'}">
		<StatusIcon {tone} spin={r.activity === 'working'} class="mt-0.5" />
		<div class="min-w-0 flex-1">
			<p class="text-sm font-medium leading-snug {dense ? 'line-clamp-1' : 'line-clamp-2'}">{r.title}</p>
			{#if !dense}
				<p class="num mt-1 flex min-w-0 items-center gap-1 overflow-hidden text-[11px] whitespace-nowrap text-muted-foreground">
					<span>{r.id}</span>{#if !session.scope}<span class="rounded bg-muted px-1">{r.project}</span>{/if}
					{#if r.branch}<GitBranchIcon class="ml-1 size-3 shrink-0" /><span class="truncate">{r.branch}</span>{/if}
				</p>
			{/if}
		</div>
		{#if r.priority <= 1}<span class="num rounded bg-st-fail/12 px-1 text-[10px] font-semibold text-st-fail">P{r.priority}</span>{/if}
	</div>
	<div class="num space-y-1 border-t {dense ? 'px-2.5 py-1.5' : 'px-3 py-2'} text-[11px]">
		<div class="flex items-center gap-1.5">
			<span class="size-1.5 shrink-0 rounded-full {BG[sub.tone]}"></span>
			<span class="min-w-0 truncate {TEXT[sub.tone]}" title={sub.text}>{sub.text}</span>
			<span class="ml-auto shrink-0 text-muted-foreground">{since(r.updatedAt)}</span>
		</div>
		{#if !dense}
			<div class="flex flex-wrap items-center gap-x-3 gap-y-0.5 text-muted-foreground [&>span]:whitespace-nowrap">
				{#if r.pr?.number}<span>PR #{r.pr.number} <span class={prTone(r.pr.state)}>{r.pr.state}</span></span>{/if}
				{#if r.machine}<span class="flex items-center gap-1"><ServerIcon class="size-3" />{r.machine}</span>{/if}
				{#if r.usage && Number(r.usage.tokens)}<span class="ml-auto">{compact(r.usage.tokens)} · {money(r.usage.costUsd)}</span>{/if}
			</div>
			{#if r.criteriaTotal}
				<div class="flex items-center gap-2 text-muted-foreground">
					<span class="h-1 flex-1 rounded-full bg-muted"><span class="block h-1 rounded-full bg-st-done" style="width:{(r.criteriaMet * 100) / r.criteriaTotal}%"></span></span>
					<span>{t('criteriaShort', r.criteriaMet, r.criteriaTotal)}</span>
				</div>
			{/if}
		{/if}
	</div>
</button>
