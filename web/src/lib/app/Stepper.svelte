<script lang="ts">
	import CheckIcon from '@lucide/svelte/icons/check';
	import type { FlowStep } from '$lib/gen/barnraise/v1/barnraise_pb';
	import { t } from '$lib/i18n';
	import type { Tone } from '$lib/status';

	// The item's flow as the project defines it, and where the item is on it:
	// both come from the daemon (ItemOverview.flow).
	let { flow, tone }: { flow: FlowStep[]; tone: Tone } = $props();
	const ring: Record<Tone, string> = {
		needs: 'border-st-needs text-st-needs', work: 'border-st-work text-st-work', review: 'border-st-review text-st-review',
		queue: 'border-st-queue text-st-queue', done: 'border-st-done text-st-done', fail: 'border-st-fail text-st-fail', idle: 'border-st-idle text-st-idle'
	};
</script>

{#if flow.length}
	<ol class="flex flex-wrap items-center gap-x-1 gap-y-2" aria-label={t('flowLabel')}>
		{#each flow as s, i (s.name)}
			<li class="flex items-center gap-1">
				<span class="flex items-center gap-2 rounded-full border px-2.5 py-1 text-xs whitespace-nowrap
					{s.state === 'done' ? 'border-st-done/40 bg-st-done/10 text-st-done' : s.state === 'current' ? ring[tone] + ' bg-background font-medium' : 'border-dashed text-muted-foreground'}"
					title={t('stepKind_' + s.kind)} aria-current={s.state === 'current' ? 'step' : undefined}>
					{#if s.state === 'done'}<CheckIcon class="size-3" />{:else}<span class="num text-[10px]">{i + 1}</span>{/if}
					{s.name}
				</span>
				{#if i < flow.length - 1}<span class="h-px w-4 {s.state === 'done' ? 'bg-st-done/50' : 'bg-border'}"></span>{/if}
			</li>
		{/each}
	</ol>
{/if}
