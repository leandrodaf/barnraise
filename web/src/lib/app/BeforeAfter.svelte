<script lang="ts">
	import ArrowRight from '@lucide/svelte/icons/arrow-right';
	import Columns from '@lucide/svelte/icons/columns-2';
	import Rows from '@lucide/svelte/icons/rows-2';
	import * as ToggleGroup from '$lib/components/ui/toggle-group';
	import { t } from '$lib/i18n';
	import type { Diagram } from '$lib/rest';
	import Mermaid from './Mermaid.svelte';

	// The design before and after the change, side by side: the pair a plan
	// draws under "Before" and "After"; any other diagram follows.
	let { diagrams }: { diagrams: Diagram[] } = $props();

	const isBefore = (d: Diagram) => /\b(before|antes|current|atual)\b/i.test(d.title);
	const isAfter = (d: Diagram) => /\b(after|depois|proposed|proposto|novo|new)\b/i.test(d.title);
	const before = $derived(diagrams.find(isBefore));
	const after = $derived(diagrams.find(isAfter));
	const rest = $derived(diagrams.filter((d) => d !== before && d !== after));
	let layout = $state<'side' | 'stack'>('side');
</script>

{#if before || after}
	<div class="space-y-3">
		{#if before && after}
			<div class="flex justify-end">
				<ToggleGroup.Root type="single" size="sm" variant="outline" bind:value={layout}>
					<ToggleGroup.Item value="side" aria-label={t('docSideBySide')} title={t('docSideBySide')}><Columns class="size-3.5" /></ToggleGroup.Item>
					<ToggleGroup.Item value="stack" aria-label={t('docStacked')} title={t('docStacked')}><Rows class="size-3.5" /></ToggleGroup.Item>
				</ToggleGroup.Root>
			</div>
		{/if}
		<div class="grid items-start gap-3 {before && after && layout === 'side' ? 'xl:grid-cols-[1fr_auto_1fr]' : ''}">
			{#if before}
				<div class="min-w-0"><Mermaid source={before.source} title={t('docBefore')} tone="before" /></div>
			{/if}
			{#if before && after && layout === 'side'}
				<div class="hidden flex-col items-center gap-1 self-center xl:flex" aria-hidden="true">
					<span class="h-10 w-px bg-gradient-to-b from-transparent to-border"></span>
					<span class="flex size-8 items-center justify-center rounded-full border bg-card shadow-sm"><ArrowRight class="size-4 text-st-done" /></span>
					<span class="h-10 w-px bg-gradient-to-t from-transparent to-border"></span>
				</div>
			{/if}
			{#if after}
				<div class="min-w-0"><Mermaid source={after.source} title={t('docAfter')} tone="after" /></div>
			{/if}
		</div>
	</div>
{/if}
{#each rest as d, i (i)}
	<div class="mt-3"><Mermaid source={d.source} title={d.title} /></div>
{/each}
