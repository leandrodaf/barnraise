<script lang="ts">
	import * as Tabs from '$lib/components/ui/tabs';
	import ChartIcon from '@lucide/svelte/icons/chart-no-axes-column';
	import TriangleAlertIcon from '@lucide/svelte/icons/triangle-alert';
	import { api, store } from '$lib/api';
	import { t } from '$lib/i18n';
	import { compact, dur, num, ratioPct } from '$lib/format';
	import { session } from '$lib/session.svelte';
	import { href } from '$lib/router.svelte';
	import { loader } from '$lib/loader.svelte';
	import Loading from '$lib/app/Loading.svelte';
	import PageHeader from '$lib/app/PageHeader.svelte';
	import PageGrid from '$lib/app/PageGrid.svelte';
	import Section from '$lib/app/Section.svelte';
	import Stat from '$lib/app/Stat.svelte';
	import Bars from '$lib/app/Bars.svelte';

	// Metrics are per project: with every project in scope, a tab picks one.
	let window = $state(store.get('window') || '7d');
	let picked = $state(session.scope || session.project);
	const project = $derived(session.scope || (session.projects.includes(picked) ? picked : session.projects[0]));
	const m = loader(() => api.metrics({ project, window }));
	let first = true;
	$effect(() => {
		void project;
		store.set('window', window);
		if (first) return void (first = false);
		m.reload();
	});
</script>

<PageHeader title={t('metrics')} icon={ChartIcon} meta={[project, t('windowN', window)]}>
	{#snippet actions()}
		<Tabs.Root bind:value={window}><Tabs.List>{#each ['1d', '7d', '30d', '90d'] as w (w)}<Tabs.Trigger value={w} class="num text-xs">{w}</Tabs.Trigger>{/each}</Tabs.List></Tabs.Root>
	{/snippet}
	{#if !session.scope && session.projects.length > 1}
		<Tabs.Root bind:value={picked}><Tabs.List>{#each session.projects as p (p)}<Tabs.Trigger value={p}>{p}</Tabs.Trigger>{/each}</Tabs.List></Tabs.Root>
	{/if}
</PageHeader>
<Loading state={m}>
	{@const x = m.data!}
	<div class="mb-4 grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4 2xl:grid-cols-7">
		<Stat label={t('integratedPerDay')} value={num(x.integratedPerDay)} />
		<Stat label={t('reworkAvg')} value={num(x.reworkAvg)} alert={x.flags.includes('rework_avg')} />
		<Stat label={t('cacheHit')} value={ratioPct(x.cacheHitRate)} />
		<Stat label={t('proposalsRate')} value={t('proposalsLine', x.proposalsApproved, x.proposalsMade)} alert={x.flags.includes('proposals')} />
		<Stat label={t('waitingMax')} value={x.waitingOwnerMax || '—'} alert={x.flags.includes('waiting_owner')} />
		<Stat label={t('violations')} value={x.violations} alert={x.flags.includes('violations')} />
		<Stat label={t('flakyQ')} value={x.flakyQuarantined} alert={x.flags.includes('flaky')} />
	</div>
	<PageGrid>
		<div class="grid gap-4 lg:grid-cols-2 min-[2000px]:grid-cols-4">
			<Section title={t('tokensByModel')}><Bars map={x.tokensByModel} fmt={compact} /></Section>
			<Section title={t('tokensByRole')}><Bars map={x.tokensByRole} fmt={compact} /></Section>
			<Section title={t('stepSeconds')}><Bars map={x.stepSeconds} fmt={dur} /></Section>
			<Section title={t('tokensByItem')}><Bars map={x.tokensByItem} fmt={compact} limit={10} link={(k) => href('item', project, k)} /></Section>
		</div>
		{#snippet aside()}
			{#if x.alerts.length}
				<Section title={t('alerts')} count={x.alerts.length}><ul class="space-y-2 text-sm">{#each x.alerts as a, i (i)}<li class="flex gap-2"><TriangleAlertIcon class="mt-0.5 size-4 shrink-0 text-st-needs" /><span class="min-w-0 break-words">{a}</span></li>{/each}</ul></Section>
			{/if}
			<Section title={t('byKind')}><Bars map={x.integratedByKind} fmt={num} /></Section>
			<Section title={t('reworkCauses')}><Bars map={x.reworkCauses} fmt={num} share /></Section>
		{/snippet}
	</PageGrid>
</Loading>
