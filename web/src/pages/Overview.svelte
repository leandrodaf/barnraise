<script lang="ts">
	import * as Table from '$lib/components/ui/table';
	import CoinsIcon from '@lucide/svelte/icons/coins';
	import ActivityIcon from '@lucide/svelte/icons/activity';
	import ServerIcon from '@lucide/svelte/icons/server';
	import ClockIcon from '@lucide/svelte/icons/clock';
	import { t } from '$lib/i18n';
	import { ago, compact, dur, money, since } from '$lib/format';
	import { cluster } from '$lib/cluster.svelte';
	import { TEXT, COLUMNS, toneOfItem, signalTone, signalText, columnLabel } from '$lib/status';
	import type { UsageTotals } from '$lib/gen/barnraise/v1/barnraise_pb';
	import { clusterItems } from '$lib/app/items';
	import { session } from '$lib/session.svelte';
	import { href } from '$lib/router.svelte';
	import PageGrid from '$lib/app/PageGrid.svelte';
	import NetworkIcon from '@lucide/svelte/icons/network';
	import { openItem } from '$lib/app/open';
	import PageHeader from '$lib/app/PageHeader.svelte';
	import Section from '$lib/app/Section.svelte';
	import Kpi from '$lib/app/Kpi.svelte';
	import Bars from '$lib/app/Bars.svelte';
	import DayChart from '$lib/app/DayChart.svelte';
	import StatusIcon from '$lib/app/StatusIcon.svelte';
	import Empty from '$lib/app/Empty.svelte';

	// The scope's overview: the daemon filters it and adds the usage up.
	const o = $derived(cluster.data);
	const rows = $derived(clusterItems(o));
	let filter = $state<string>('all');
	const FILTERS = ['all', ...COLUMNS];
	const shown = $derived(rows.filter((r) => filter === 'all' || r.column === filter));
	const count = (f: string) => (f === 'all' ? rows.length : rows.filter((r) => r.column === f).length);
	const cost = (m: Record<string, UsageTotals> | undefined) => Object.fromEntries(Object.entries(m || {}).map(([k, u]) => [k, u.costUsd]));
	const byDay = $derived(Object.entries(o?.usageByDay || {}).sort(([a], [b]) => a.localeCompare(b)));
	const byItem = $derived(Object.fromEntries(rows.filter((r) => r.usage).map((r) => [r.project + '/' + r.id, r.usage!.costUsd])));
</script>

<PageHeader title={t('navOverview')} icon={NetworkIcon} meta={[session.scope || t('allProjects'), o ? t('overviewFrom', o.machine, ago(o.generatedAt)) : '', t('queueCount', rows.length)]} />
{#if !o}<Empty text={t('loading')} />{:else}
	{@const u = o.usage}
	<div class="mb-4 grid grid-cols-2 gap-3 lg:grid-cols-4">
		<Kpi label={t('ovCost')} value={money(u?.costUsd)} tone="needs" icon={CoinsIcon} hint={t('ovTokensHint', compact(u?.tokens), u?.sessions || 0)} />
		<Kpi label={t('ovAgentTime')} value={dur(u?.seconds)} tone="work" icon={ClockIcon} hint={t('ovCpuHint', dur(u?.cpuSeconds))} />
		<Kpi label={t('ovMachines')} value={o.machines.length} tone="done" icon={ServerIcon} hint={o.machines.map((m) => m.machine?.name).join(', ')} />
		<Kpi label={t('ovSessions')} value={o.machines.reduce((s, m) => s + m.sessions.length, 0)} tone="review" icon={ActivityIcon} hint={t('kpiNow')} />
	</div>

	<div class="mb-4 grid gap-4 lg:grid-cols-2 min-[1900px]:grid-cols-3">
		<Section title={t('ovByDay')} sub={t('ovByDayHint')}><DayChart days={byDay} />{#if !byDay.length}<Empty />{/if}</Section>
		<Section title={t('ovByItem')}><Bars map={byItem} fmt={money} limit={8} link={(k) => href('item', ...k.split('/'))} name={(k) => (session.scope ? k.split('/')[1] : k.replace('/', ' · '))} /></Section>
		<Section title={t('ovByModel')}><Bars map={cost(o.usageByModel)} fmt={money} share /></Section>
		<Section title={t('ovByMachine')}><Bars map={cost(o.usageByMachine)} fmt={money} share /></Section>
		<Section title={t('ovByAccount')}><Bars map={cost(o.usageByAccount)} fmt={money} share /></Section>
		<Section title={t('ovByRole')}><Bars map={cost(o.usageByRole)} fmt={money} share /></Section>
	</div>

	<Section title={t('ovAllItems')} count={shown.length} pad={false}>
		{#snippet right()}
			<div class="flex max-w-full gap-1 overflow-x-auto">
				{#each FILTERS as f (f)}
					<button class="num shrink-0 rounded-md px-2 py-1 text-[11px] whitespace-nowrap transition-colors {filter === f ? 'bg-foreground text-background' : 'text-muted-foreground hover:bg-muted'}" onclick={() => (filter = f)}>{f === 'all' ? t('all') : columnLabel(f)} {count(f)}</button>
				{/each}
			</div>
		{/snippet}
		<div class="overflow-x-auto"><Table.Root>
			<Table.Header><Table.Row class="eyebrow">
				<Table.Head>{t('item')}</Table.Head><Table.Head>{t('ovActivity')}</Table.Head><Table.Head>{t('step')}</Table.Head><Table.Head>{t('machine')}</Table.Head>
				<Table.Head>PR</Table.Head><Table.Head class="text-right">{t('tokens')}</Table.Head><Table.Head class="text-right">{t('ovCost')}</Table.Head><Table.Head class="text-right">{t('updated')}</Table.Head>
			</Table.Row></Table.Header>
			<Table.Body>
				{#each shown as r (r.project + r.id)}
					{@const tone = toneOfItem(r)}
					{@const sub = { text: signalText(r), tone: signalTone(r) }}
					<Table.Row class="cursor-pointer" onclick={() => openItem(r.project, r.id)}>
						<Table.Cell class="min-w-72 max-w-lg">
							<div class="flex items-start gap-2"><StatusIcon {tone} spin={r.activity === 'working'} class="mt-0.5" /><div class="min-w-0"><p class="truncate font-medium" title={r.title}>{r.title}</p><p class="num text-[11px] text-muted-foreground">{r.id}{session.scope ? '' : ' · ' + r.project}</p></div></div>
						</Table.Cell>
						<Table.Cell><p class="num max-w-60 truncate text-xs {TEXT[sub.tone]}" title={sub.text}>{sub.text}</p></Table.Cell>
						<Table.Cell class="num text-xs">{r.step}</Table.Cell>
						<Table.Cell class="num text-xs">{r.machine}</Table.Cell>
						<Table.Cell class="num text-xs">{#if r.pr?.number}#{r.pr.number} · {r.pr.state}{/if}</Table.Cell>
						<Table.Cell class="num text-right text-xs">{compact(r.usage?.tokens)}</Table.Cell>
						<Table.Cell class="num text-right text-xs">{money(r.usage?.costUsd)}</Table.Cell>
						<Table.Cell class="num text-right text-xs text-muted-foreground">{since(r.updatedAt)}</Table.Cell>
					</Table.Row>
				{/each}
			</Table.Body>
		</Table.Root></div>
	</Section>
{/if}
