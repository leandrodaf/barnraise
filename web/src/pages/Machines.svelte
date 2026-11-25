<script lang="ts">
	import { Switch } from '$lib/components/ui/switch';
	import { Badge } from '$lib/components/ui/badge';
	import MoonStarIcon from '@lucide/svelte/icons/moon-star';
	import CpuIcon from '@lucide/svelte/icons/cpu';
	import { t } from '$lib/i18n';
	import { ago, bytes, compact, dur, money, num, pct } from '$lib/format';
	import { session } from '$lib/session.svelte';
	import { cluster } from '$lib/cluster.svelte';
	import { standby } from '$lib/app/actions';
	import PageHeader from '$lib/app/PageHeader.svelte';
	import ServerIcon from '@lucide/svelte/icons/server';
	import Sparkline from '$lib/app/Sparkline.svelte';
	import Dot from '$lib/app/Dot.svelte';
	import Empty from '$lib/app/Empty.svelte';
	import { Button } from '$lib/components/ui/button';
	import MinusIcon from '@lucide/svelte/icons/minus';
	import PlusIcon from '@lucide/svelte/icons/plus';
	import { toast } from 'svelte-sonner';
	import { api } from '$lib/api';
	import { act } from '$lib/app/act';
	import ActivityIcon from '@lucide/svelte/icons/activity';
	import ChevronDownIcon from '@lucide/svelte/icons/chevron-down';
	import { procs, procRows } from '$lib/procs.svelte';
	import { href } from '$lib/router.svelte';
	import ProcSummary from '$lib/app/ProcSummary.svelte';
	import ProcTable from '$lib/app/ProcTable.svelte';
	import WorktreeList from '$lib/app/WorktreeList.svelte';

	// The processes of each machine come from the detailed overview, read every few seconds.
	procs.use();
	const detail = (name: string) => procs.data?.machines.find((x) => x.machine?.name === name);
	const rowsOf = (name: string) => procRows(procs.data).filter((r) => r.machine === name);
	let openWt = $state<Record<string, boolean>>({});

	// Every machine of the cluster, from the overview: the header's project
	// does not narrow this screen.
	const list = $derived(cluster.data?.machines || []);
	const cpuPct = (m: { load1: number; cpus: number }) => (m.cpus ? Math.min(100, (m.load1 / m.cpus) * 100) : 0);
	const memPct = (m: { memGb: number; freeMemGb: number }) => (m.memGb && m.freeMemGb > 0 ? ((m.memGb - m.freeMemGb) / m.memGb) * 100 : 0);
	let busyLimit = $state(false);
	// Another machine takes a project of this one: it clones the repository and registers it.
	let sharing = $state('');
	async function share(project: string, machine: string) {
		sharing = machine + project;
		const r = await act(() => api.shareProject({ project, machines: [machine] }));
		const res = r?.machines.find((x) => x.name === machine);
		if (res && !res.ok) toast.error(t('error', res.error));
		else if (res) toast.success(t('mShared', machine, project));
		sharing = '';
		setTimeout(() => cluster.read(), 2000);
	}
	async function setLimit(machine: string, n: number) {
		busyLimit = true;
		const r = await act(() => api.setMaxAgents({ maxAgents: n, machines: [machine] }));
		const res = r?.machines.find((x) => x.name === machine);
		if (res && !res.ok) toast.error(t('error', res.error));
		else if (res) toast.success(n ? t('mLimitSet', machine, n) : t('mLimitAuto', machine));
		busyLimit = false;
		setTimeout(() => cluster.read(), 1500);
	}
	const health = (m: { health: string; standby: boolean; left: boolean }) => (m.left ? 'idle' : m.standby ? 'needs' : m.health === 'green' ? 'done' : m.health === 'red' ? 'fail' : 'needs');
</script>

<PageHeader title={t('machines')} icon={ServerIcon} meta={[t('machinesN', list.length), t('standbyN', list.filter((m) => m.machine?.standby).length), t('sbDaemon', session.version, session.machine)]} subtitle={t('machinesHelp')} />

{#if !cluster.data}<Empty text={t('loading')} />{:else if !list.length}<Empty text={t('noMachines')} />{/if}
<div class="grid gap-4 2xl:grid-cols-2 min-[2400px]:grid-cols-3">
	{#each list as mo (mo.machine?.name)}
		{@const m = mo.machine!}
		{@const h = cluster.history[m.name]?.samples || []}
		{@const self = m.name === session.machine}
			{@const pr = rowsOf(m.name)}
			{@const d = detail(m.name)}
		<article class="overflow-hidden rounded-xl border bg-card {m.standby ? 'border-dashed border-st-needs/60' : ''} {m.left ? 'opacity-60' : ''}">
			<header class="flex flex-wrap items-center gap-x-3 gap-y-2 border-b px-4 py-3">
				<Dot tone={health(m)} pulse={!m.standby && mo.sessions.length > 0} />
				<div class="min-w-0 flex-1">
					<p class="flex flex-wrap items-center gap-x-2 font-semibold"><span class="break-all">{m.name}</span>{#if self}<Badge variant="outline" class="h-4 px-1.5 text-[10px]">{t('thisMachine')}</Badge>{/if}</p>
					<p class="num text-[11px] text-muted-foreground">{m.platform} · {m.version} · {t('lastSeenShort', ago(m.lastSeen))}</p>
				</div>
				{#if self}
					<label class="flex items-center gap-2 rounded-lg border px-3 py-1.5 text-sm {m.standby ? 'border-st-needs/50 bg-st-needs/10' : ''}">
						<MoonStarIcon class="size-4 {m.standby ? 'text-st-needs' : 'text-muted-foreground'}" />{t('standbyBadge')}
						<Switch checked={m.standby} onCheckedChange={(v) => standby(v)} aria-label={t('standbyBadge')} />
					</label>
				{:else if m.standby}
					<span class="flex items-center gap-1.5 rounded-lg border border-st-needs/50 bg-st-needs/10 px-3 py-1.5 text-sm text-st-needs"><MoonStarIcon class="size-4" />{t('standbyBadge')}</span>
				{/if}
			</header>
			{#if m.standby}<p class="border-b bg-st-needs/5 px-4 py-2 text-xs text-muted-foreground">{t('standbyCard')}{#if self && session.standbyWhy} · {session.standbyWhy}{/if}</p>{/if}
			<div class="grid grid-cols-2 gap-px bg-border sm:grid-cols-4 {h.some((x) => x.gpuPct > 0) ? 'xl:grid-cols-5' : ''}">
				{#each [
					{ k: 'cpu', v: cpuPct(m), sub: t('mCores', m.cpus) + ' · ' + t('cpuSub', num(m.load1), m.cpus), series: h.map((x) => x.cpuPct), tone: 'work' },
					{ k: 'ram', v: memPct(m), sub: t('memoryUsed', num(Math.max(0, m.memGb - m.freeMemGb)), num(m.memGb)), series: h.map((x) => x.memPct), tone: 'review' },
					{ k: t('mAgentsLabel'), v: m.slots ? (m.busy * 100) / m.slots : 0, sub: t('mAgents', m.busy, m.slots), series: h.map((x) => (x.slots ? (x.busy * 100) / x.slots : 0)), tone: 'done' },
					...(h.some((x) => x.gpuPct > 0) ? [{ k: 'gpu', v: h[h.length - 1]?.gpuPct || 0, sub: h[h.length - 1]?.gpuTempC ? t('degrees', num(h[h.length - 1].gpuTempC)) : '', series: h.map((x) => x.gpuPct), tone: 'needs' }] : []),
				{ k: 'disk', v: Number(mo.diskTotalBytes) ? (1 - Number(mo.diskFreeBytes) / Number(mo.diskTotalBytes)) * 100 : 0, sub: t('diskSub', bytes(mo.diskFreeBytes)), series: [], tone: 'queue' }
				] as g (g.k)}
					<div class="space-y-1 bg-card p-3">
						<p class="eyebrow flex justify-between text-muted-foreground"><span>{g.k}</span><span class="num text-foreground">{pct(g.v)}</span></p>
						<div class="h-8">{#if g.series.length > 1}<Sparkline values={g.series} tone={g.tone} height={32} label={g.k} />{:else}<div class="mt-3 h-1.5 rounded-full bg-muted"><div class="h-1.5 rounded-full" style="width:{g.v}%;background:var(--st-{g.tone})"></div></div>{/if}</div>
						<p class="num truncate text-[11px] text-muted-foreground">{g.sub}</p>
					</div>
				{/each}
			</div>
			<!-- How many agents at once, and why that number. -->
			<div class="flex flex-wrap items-center gap-x-4 gap-y-2 border-t px-4 py-3 text-sm">
				<div class="min-w-0 flex-1">
					<p>{t('mUpTo', m.slots)}</p>
					<p class="text-xs text-muted-foreground">{m.slotsBy === 'owner' ? t('mLimitOwner', m.autoSlots) : t('mLimit_' + (m.slotsBy || 'cpu'), m.cpus, num(m.memGb))}</p>
				</div>
				<div class="flex items-center gap-1" role="group" aria-label={t('mSetLimit')}>
					<Button size="icon" variant="outline" class="size-8" disabled={busyLimit || m.slots <= 1} onclick={() => setLimit(m.name, m.slots - 1)} aria-label={t('mFewer')}><MinusIcon class="size-3.5" /></Button>
					<span class="num w-8 text-center font-semibold">{m.slots}</span>
					<Button size="icon" variant="outline" class="size-8" disabled={busyLimit || m.slots >= 64} onclick={() => setLimit(m.name, m.slots + 1)} aria-label={t('mMoreAgents')}><PlusIcon class="size-3.5" /></Button>
					{#if m.slotsBy === 'owner'}<Button size="sm" variant="ghost" disabled={busyLimit} onclick={() => setLimit(m.name, 0)}>{t('mAuto')}</Button>{/if}
				</div>
			</div>
			<div class="space-y-2 border-t px-4 py-3">
				<p class="eyebrow flex items-center gap-1.5 text-muted-foreground"><ActivityIcon class="size-3" />{t('procTitle')}{#if d?.reportedAt}<span class="num ml-auto normal-case tracking-normal">{t('procReported', ago(d.reportedAt))}</span>{/if}</p>
				{#if d && !d.procsSupported}<p class="text-xs text-muted-foreground">{t('procNotHere')}</p>{:else}<ProcSummary p={d?.procs || mo.procs} cores={m.cpus} />{/if}
			</div>
			{#if pr.length}
				<div class="border-t"><ProcTable rows={pr} where={false} limit={6} /></div>
				<a href={href('processes')} class="block border-t px-4 py-1.5 text-[11px] text-muted-foreground hover:bg-muted/40 hover:text-foreground">{t('procSeeAll', pr.length)}</a>
			{/if}
			{#if mo.worktrees.length}
				<button class="flex w-full items-center gap-1.5 border-t px-4 py-2 text-left text-xs text-muted-foreground hover:bg-muted/40" onclick={() => (openWt[m.name] = !openWt[m.name])}>
					<ChevronDownIcon class="size-3.5 transition-transform {openWt[m.name] ? 'rotate-180' : ''}" />{t('ovWorktrees')} <span class="num">{mo.worktrees.length} · {bytes(mo.worktreeBytes)}</span>
					<span class="num ml-auto">{t('wtInUseN', mo.worktrees.filter((w) => w.inUse).length)}</span>
				</button>
				{#if openWt[m.name]}<div class="border-t"><WorktreeList list={d?.worktrees || mo.worktrees} machine={false} /></div>{/if}
			{/if}
			<div class="num grid gap-x-6 gap-y-1 border-t px-4 py-3 text-xs sm:grid-cols-2">
				<p class="flex justify-between"><span class="text-muted-foreground">{t('ovSessions')}</span><span>{mo.sessions.length}</span></p>
				<p class="flex justify-between"><span class="text-muted-foreground">{t('ovItems')}</span><span>{mo.items.length}</span></p>
				<p class="flex justify-between"><span class="text-muted-foreground">{t('ovWorktrees')}</span><span>{mo.worktrees.length} · {bytes(mo.worktreeBytes)}</span></p>
				{#if m.tempC > 0}<p class="flex justify-between"><span class="text-muted-foreground">{t('tempLabel')}</span><span>{t('degrees', num(m.tempC))}</span></p>{/if}
				{#each m.gpus as g (g.name)}<p class="flex justify-between gap-2 sm:col-span-2"><span class="flex items-center gap-1 text-muted-foreground"><CpuIcon class="size-3" />{g.name}</span><span>{pct(g.utilPct)} · {num(g.memUsedGb)}/{num(g.memTotalGb)} GB{g.tempC > 0 ? ' · ' + t('degrees', num(g.tempC)) : ''}</span></p>{/each}
				{#if mo.usage}<p class="flex justify-between sm:col-span-2"><span class="text-muted-foreground">{t('ovSpentHereLabel')}</span><span>{compact(mo.usage.tokens)} · {dur(mo.usage.seconds)} · {money(mo.usage.costUsd)}</span></p>{/if}
				{#if m.accounts.length}<p class="flex justify-between sm:col-span-2"><span class="text-muted-foreground">{t('accounts')}</span><span class="truncate">{m.accounts.join(', ')}</span></p>{/if}
				{#if mo.projects.length}<p class="flex justify-between gap-2 sm:col-span-2"><span class="text-muted-foreground">{t('projects')}</span><span class="truncate">{mo.projects.join(', ')}</span></p>{/if}
				{#if !self && session.projects.some((p) => !mo.projects.includes(p))}
					<div class="flex flex-wrap items-center gap-2 pt-1 sm:col-span-2">
						<span class="text-muted-foreground">{t('mShareHelp')}</span>
						{#each session.projects.filter((p) => !mo.projects.includes(p)) as p (p)}
							<Button size="sm" variant="outline" class="h-7" disabled={sharing === m.name + p} onclick={() => share(p, m.name)}>{sharing === m.name + p ? t('mSharing') : t('mShare', p)}</Button>
						{/each}
					</div>
				{/if}
			</div>
		</article>
	{/each}
</div>
