<script lang="ts">
	import CpuIcon from '@lucide/svelte/icons/cpu';
	import MemoryStickIcon from '@lucide/svelte/icons/memory-stick';
	import ActivityIcon from '@lucide/svelte/icons/activity';
	import FolderGitIcon from '@lucide/svelte/icons/folder-git-2';
	import SearchIcon from '@lucide/svelte/icons/search';
	import { Input } from '$lib/components/ui/input';
	import { t } from '$lib/i18n';
	import { ago, bytes, dur, num, pct } from '$lib/format';
	import { SOFT } from '$lib/status';
	import { session } from '$lib/session.svelte';
	import { procs, procRows, worktreesOf, kindTone } from '$lib/procs.svelte';
	import PageHeader from '$lib/app/PageHeader.svelte';
	import PageGrid from '$lib/app/PageGrid.svelte';
	import Section from '$lib/app/Section.svelte';
	import Kpi from '$lib/app/Kpi.svelte';
	import Bars from '$lib/app/Bars.svelte';
	import ProcTable from '$lib/app/ProcTable.svelte';
	import ProcSummary from '$lib/app/ProcSummary.svelte';
	import WorktreeList from '$lib/app/WorktreeList.svelte';
	import Empty from '$lib/app/Empty.svelte';
	import Dot from '$lib/app/Dot.svelte';

	// Everything the agents started, on every machine: the CLIs, the tests,
	// the browsers, the builds, the emulators. Read every few seconds.
	procs.use();
	const o = $derived(procs.data);
	const machines = $derived(o?.machines || []);
	const all = $derived(procRows(o));
	const wts = $derived(worktreesOf(o));
	let kind = $state('');
	let machine = $state('');
	let q = $state('');
	const rows = $derived(
		all.filter((p) => (!kind || p.kind === kind) && (!machine || p.machine === machine) && (!q || (p.command + ' ' + p.name + ' ' + p.itemId + ' ' + p.pid).toLowerCase().includes(q.toLowerCase())))
	);
	const tot = $derived.by(() => {
		let count = 0, cpu = 0, rss = 0n, cpuSec = 0, peak = 0n, cores = 0;
		const byKind: Record<string, number> = {};
		for (const m of machines) {
			cores += m.machine?.cpus || 0;
			const p = m.procs;
			if (!p) continue;
			count += p.count; cpu += p.cpuPct; rss += p.rssBytes; cpuSec += p.cpuSeconds;
			if (p.peakRssBytes > peak) peak = p.peakRssBytes;
			for (const [k, n] of Object.entries(p.byKind)) byKind[k] = (byKind[k] || 0) + n;
		}
		return { count, cpu, rss, cpuSec, peak, cores, byKind };
	});
	const cpuByMachine = $derived(Object.fromEntries(machines.map((m) => [m.machine?.name || '—', m.procs?.cpuPct || 0])));
	const ramByItem = $derived.by(() => {
		const m: Record<string, number> = {};
		for (const w of wts) if (w.procs?.rssBytes) m[w.itemId ? w.project + '/' + w.itemId : w.project + ' · ' + w.name] = (m[w.itemId ? w.project + '/' + w.itemId : w.project + ' · ' + w.name] || 0) + Number(w.procs.rssBytes);
		return m;
	});
	const unsupported = $derived(machines.filter((m) => !m.procsSupported).map((m) => m.machine?.name));
	const kinds = $derived(Object.entries(tot.byKind).sort((a, b) => b[1] - a[1]));
</script>

<PageHeader title={t('navProcesses')} icon={ActivityIcon} meta={[session.scope || t('allProjects'), procs.at ? t('procUpdated', ago(new Date(procs.at).toISOString())) : t('loading'), t('procEvery')]} subtitle={t('procHelp')} />

{#if !o}<Empty text={procs.error || t('loading')} />{:else}
	<div class="mb-4 grid grid-cols-2 gap-3 lg:grid-cols-4">
		<Kpi label={t('procCount')} value={num(tot.count)} tone="work" icon={ActivityIcon} hint={kinds.slice(0, 3).map(([k, n]) => t('pk_' + k) + ' ' + n).join(' · ')} />
		<Kpi label={t('procCpuNow')} value={pct(tot.cpu)} tone="review" icon={CpuIcon} hint={tot.cores ? t('procOfCluster', pct(tot.cpu / tot.cores), tot.cores) : ''} />
		<Kpi label={t('procRamNow')} value={bytes(tot.rss)} tone="needs" icon={MemoryStickIcon} hint={t('procPeak', bytes(tot.peak))} />
		<Kpi label={t('ovWorktrees')} value={wts.length} tone="done" icon={FolderGitIcon} hint={t('wtInUseN', wts.filter((w) => w.inUse).length) + ' · ' + t('procCpuTimeTotal', dur(tot.cpuSec))} />
	</div>
	{#if unsupported.length}<p class="mb-4 rounded-lg border border-dashed px-3 py-2 text-xs text-muted-foreground">{t('procUnsupported', unsupported.join(', '))}</p>{/if}

	<PageGrid>
		<Section title={t('procTop')} count={rows.length} pad={false}>
			{#snippet right()}
				<div class="flex flex-wrap items-center gap-1.5">
					<div class="relative"><SearchIcon class="absolute left-2 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" /><Input bind:value={q} placeholder={t('procSearch')} class="h-7 w-44 pl-7 text-xs" /></div>
					{#if machines.length > 1}
						<select bind:value={machine} class="h-7 rounded-md border bg-background px-2 text-xs" aria-label={t('machine')}>
							<option value="">{t('allMachines')}</option>
							{#each machines as m (m.machine?.name)}<option value={m.machine?.name}>{m.machine?.name}</option>{/each}
						</select>
					{/if}
				</div>
			{/snippet}
			<div class="flex flex-wrap gap-1 border-b px-3 py-2">
				<button class="num rounded-md px-2 py-1 text-[11px] {kind === '' ? 'bg-foreground text-background' : 'text-muted-foreground hover:bg-muted'}" onclick={() => (kind = '')}>{t('all')} {all.length}</button>
				{#each kinds as [k, n] (k)}<button class="num rounded-md px-2 py-1 text-[11px] {kind === k ? 'bg-foreground text-background' : SOFT[kindTone(k)] + ' hover:opacity-80'}" onclick={() => (kind = kind === k ? '' : k)}>{t('pk_' + k)} {n}</button>{/each}
			</div>
			<ProcTable {rows} />
			{#if all.length && tot.count > all.length}<p class="border-t px-4 py-2 text-[11px] text-muted-foreground">{t('procTopOnly', all.length, tot.count)}</p>{/if}
		</Section>

		<Section title={t('ovWorktrees')} count={wts.length} pad={false}><WorktreeList list={wts} /></Section>

		{#snippet aside()}
			<Section title={t('procByMachine')}>
				<div class="space-y-4">
					{#each machines as m (m.machine?.name)}
						<div class="space-y-1.5">
							<p class="flex items-center gap-2 text-sm font-medium"><Dot tone={m.sessions.length ? 'work' : 'idle'} pulse={m.sessions.length > 0} />{m.machine?.name}<span class="num ml-auto text-[11px] font-normal text-muted-foreground">{m.reportedAt ? ago(m.reportedAt) : ''}</span></p>
							<ProcSummary p={m.procs} cores={m.machine?.cpus} compact />
						</div>
					{:else}<Empty />{/each}
				</div>
			</Section>
			<Section title={t('procCpuByMachine')}><Bars map={cpuByMachine} fmt={pct} share /></Section>
			<Section title={t('procRamByItem')}><Bars map={ramByItem} fmt={bytes} limit={10} /></Section>
		{/snippet}
	</PageGrid>
{/if}
