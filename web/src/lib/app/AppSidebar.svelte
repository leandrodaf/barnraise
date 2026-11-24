<script lang="ts">
	import BrandMark from '$lib/app/BrandMark.svelte';
	import * as Sidebar from '$lib/components/ui/sidebar';
	import { Kbd } from '$lib/components/ui/kbd';
	import PlusIcon from '@lucide/svelte/icons/plus';
	import SearchIcon from '@lucide/svelte/icons/search';
	import { t } from '$lib/i18n';
	import { since } from '$lib/format';
	import { router, href, type RouteName } from '$lib/router.svelte';
	import { session } from '$lib/session.svelte';
	import { cluster } from '$lib/cluster.svelte';
	import { toneOfItem, type Tone } from '$lib/status';
	import { NAV_GROUPS } from './nav';
	import { store } from '$lib/api';
	import ChevronRightIcon from '@lucide/svelte/icons/chevron-right';
	import * as Collapsible from '$lib/components/ui/collapsible';
	import { keys } from './keys.svelte';
	import { clusterItems, groups, type Row } from './items';
	import { openItem } from './open';
	import { newItem } from './actions';
	import Dot from './Dot.svelte';
	import UserMenu from './UserMenu.svelte';

	const g = $derived(groups(clusterItems(cluster.data)));
	const sections = $derived([
		{ key: 'sbNeeds', rows: g.needs, tone: 'needs' as Tone, age: (r: Row) => since(r.updatedAt) },
		{ key: 'sbWorking', rows: g.working, tone: 'work' as Tone, age: (r: Row) => since(r.stepStartedAt) },
		{ key: 'sbBlocked', rows: g.blocked, tone: 'fail' as Tone, age: (r: Row) => since(r.updatedAt) },
		{ key: 'sbRecent', rows: g.recent.slice(0, 5), tone: 'done' as Tone, age: (r: Row) => since(r.integratedAt) }
	].filter((s) => s.rows.length));
	// Live counts next to the screens, all from the daemon's answers.
	const openProposals = $derived((cluster.data?.projects || []).reduce((n, p) => n + p.openProposals, 0));
	const openItems = $derived(clusterItems(cluster.data).filter((r) => r.column !== 'done').length);
	const standbyMachines = $derived((cluster.all?.machines || []).filter((m) => m.machine?.standby).length);
	const limitedAccounts = $derived.by(() => {
		const ids = new Set<string>();
		for (const p of cluster.data?.projects || []) for (const a of p.accounts) if (a.outlook?.limited) ids.add(a.id);
		return ids.size;
	});
	type Badge = { n: number | string; cls: string; title?: string };
	const badge = (name: RouteName): Badge | null => {
		switch (name) {
			case 'home': return g.needs.length ? { n: g.needs.length, cls: 'bg-st-needs/15 text-st-needs' } : null;
			case 'queue': return openItems ? { n: openItems, cls: 'bg-muted text-muted-foreground' } : null;
			case 'proposals': return openProposals ? { n: openProposals, cls: 'bg-st-review/15 text-st-review' } : null;
			case 'agents': return g.working.length ? { n: g.working.length, cls: 'bg-st-work/15 text-st-work' } : null;
			case 'machines': return standbyMachines ? { n: standbyMachines, cls: 'bg-st-needs/15 text-st-needs', title: t('standbyN', standbyMachines) } : null;
			case 'accounts': return limitedAccounts ? { n: limitedAccounts, cls: 'bg-st-fail/15 text-st-fail', title: t('limitedN', limitedAccounts) } : null;
		}
		return null;
	};
	// The live lists fold; the menu keeps how the owner left them.
	let folded = $state<Record<string, boolean>>(JSON.parse(store.get('sidebarFolded') || '{}'));
	const fold = (key: string, open: boolean) => {
		folded = { ...folded, [key]: !open };
		store.set('sidebarFolded', JSON.stringify(folded));
	};
	const active = (n: RouteName) => router.current.name === n || (n === 'queue' && router.current.name === 'item');
	const machines = $derived(cluster.all?.machines.map((m) => m.machine!).filter(Boolean) || []);
	// With many machines the footer stays small: the busiest first, the rest
	// behind a link; the totals say the cluster at a glance.
	const SHOWN = 4;
	const ordered = $derived([...machines].sort((a, b) => b.busy - a.busy || Number(a.standby) - Number(b.standby) || a.name.localeCompare(b.name)));
	const busyAll = $derived(machines.reduce((n, m) => n + m.busy, 0));
	const slotsAll = $derived(machines.reduce((n, m) => n + (m.standby ? 0 : m.slots), 0));
	const memUsed = (m: { memGb: number; freeMemGb: number }) => (m.memGb && m.freeMemGb > 0 ? m.memGb - m.freeMemGb : 0);
	const machineTitle = (m: { name: string; cpus: number; memGb: number; freeMemGb: number; slots: number; busy: number; slotsBy: string; standby: boolean }) =>
		[m.name, t('mCores', m.cpus) + ' · ' + t('mMem', fmt1(memUsed(m)), fmt1(m.memGb)), m.standby ? t('mStandby') : t('mAgents', m.busy, m.slots) + ' · ' + t('mLimit_' + (m.slotsBy || 'cpu'))].join('\n');
	const fmt1 = (n: number) => (Math.round(n * 10) / 10).toLocaleString(undefined, { maximumFractionDigits: 1 });
	const cpuPct = (m: { load1: number; cpus: number }) => (m.cpus ? Math.min(100, (m.load1 / m.cpus) * 100) : 0);
	const memPct = (m: { memGb: number; freeMemGb: number }) => (m.memGb && m.freeMemGb > 0 ? ((m.memGb - m.freeMemGb) / m.memGb) * 100 : 0);
</script>

<Sidebar.Root collapsible="icon" class="h-svh">
	<Sidebar.Header class="gap-2">
		<a href={href('home')} class="flex items-center gap-2.5 rounded-md px-1.5 py-1">
			<BrandMark class="size-7 shrink-0" />
			<span class="grid leading-tight group-data-[collapsible=icon]:hidden">
				<span class="text-sm font-semibold">Barnraise</span>
				<span class="flex min-w-0 items-center gap-1.5 text-[11px] text-muted-foreground"><span class="num max-w-[7.5rem] shrink-0 truncate rounded border px-1 py-px whitespace-nowrap {session.outdated ? 'border-st-needs/50 text-st-needs' : ''}" title={t('versionLabel')}>v{session.version}</span><span class="truncate">{session.machine}</span></span>
			</span>
		</a>
		<div class="flex gap-1.5 group-data-[collapsible=icon]:flex-col">
			<button class="flex h-8 flex-1 items-center justify-center gap-1.5 rounded-md bg-foreground px-2 text-sm font-medium text-background transition-opacity hover:opacity-90 group-data-[collapsible=icon]:size-8 group-data-[collapsible=icon]:flex-none" onclick={newItem} title={t('newItem')}>
				<PlusIcon class="size-4" /><span class="group-data-[collapsible=icon]:hidden">{t('newItem')}</span>
			</button>
		</div>
		<button class="flex h-8 items-center gap-2 rounded-md border bg-background/40 px-2 text-sm text-muted-foreground transition-colors hover:bg-sidebar-accent group-data-[collapsible=icon]:size-8 group-data-[collapsible=icon]:justify-center group-data-[collapsible=icon]:px-0" onclick={() => (keys.palette = true)} title={t('paletteSearch')}>
			<SearchIcon class="size-4 shrink-0" /><span class="flex-1 text-left group-data-[collapsible=icon]:hidden">{t('search')}</span><Kbd class="group-data-[collapsible=icon]:hidden">Ctrl K</Kbd>
		</button>
	</Sidebar.Header>
	<Sidebar.Content class="overflow-x-hidden">
		{#each NAV_GROUPS as grp (grp.label)}
			<Sidebar.Group class="py-1">
				<Sidebar.GroupLabel class="eyebrow h-6">{t(grp.label)}</Sidebar.GroupLabel>
				<Sidebar.Menu>
					{#each grp.items as n (n.name)}
						{@const b = badge(n.name)}
						<Sidebar.MenuItem>
							<Sidebar.MenuButton isActive={active(n.name)} tooltipContent={t(n.label)}>
								{#snippet child({ props })}<a href={href(n.name)} {...props} aria-current={active(n.name) ? 'page' : undefined}><n.icon /><span>{t(n.label)}</span></a>{/snippet}
							</Sidebar.MenuButton>
							{#if b}<Sidebar.MenuBadge class="num {b.cls}" title={b.title}>{b.n}</Sidebar.MenuBadge>{/if}
						</Sidebar.MenuItem>
					{/each}
				</Sidebar.Menu>
			</Sidebar.Group>
		{/each}
		{#if sections.length}<Sidebar.Separator class="my-1 group-data-[collapsible=icon]:hidden" />{/if}
		{#each sections as s (s.key)}
			<Collapsible.Root open={!folded[s.key]} onOpenChange={(o) => fold(s.key, o)} class="group/fold">
				<Sidebar.Group class="py-1 group-data-[collapsible=icon]:hidden">
					<Sidebar.GroupLabel class="eyebrow h-6">
						{#snippet child({ props })}
							<Collapsible.Trigger {...props} class="{props.class} w-full hover:text-foreground">
								<ChevronRightIcon class="mr-1 size-3 transition-transform group-data-[state=open]/fold:rotate-90" />{t(s.key)}<span class="num ml-auto">{s.rows.length}</span>
							</Collapsible.Trigger>
						{/snippet}
					</Sidebar.GroupLabel>
					<Collapsible.Content>
						<Sidebar.Menu>
							{#each s.rows.slice(0, 6) as r (r.project + r.id)}
								<Sidebar.MenuItem>
									<Sidebar.MenuButton size="sm" class="h-7" onclick={() => openItem(r.project, r.id)} title={r.id + ' · ' + r.title + ' · ' + r.project}>
										<Dot tone={toneOfItem(r)} pulse={s.tone === 'work'} />
										<span class="truncate">{r.title}</span>
										<span class="num ml-auto shrink-0 text-[11px] text-muted-foreground">{s.age(r)}</span>
									</Sidebar.MenuButton>
								</Sidebar.MenuItem>
							{/each}
							{#if s.rows.length > 6}<Sidebar.MenuItem><a class="px-2 text-xs text-muted-foreground hover:text-foreground" href={href(s.key === 'sbRecent' ? 'overview' : 'queue')}>{t('more', s.rows.length - 6)}</a></Sidebar.MenuItem>{/if}
						</Sidebar.Menu>
					</Collapsible.Content>
				</Sidebar.Group>
			</Collapsible.Root>
		{/each}
	</Sidebar.Content>
	<Sidebar.Footer class="gap-2">
		<div class="space-y-2 rounded-lg border bg-background/40 p-2 group-data-[collapsible=icon]:hidden">
			<a href={href('machines')} class="flex items-baseline justify-between gap-2 text-[11px]">
				<span class="font-medium">{t('mMachinesN', machines.length)}</span>
				<span class="num text-muted-foreground" title={t('mAgentsHelp')}>{t('mAgentsAll', busyAll, slotsAll)}</span>
			</a>
			{#each ordered.slice(0, SHOWN) as m (m.name)}
				<a href={href('machines')} class="block space-y-1 rounded-md px-1 py-0.5 text-[11px] hover:bg-muted/60" title={machineTitle(m)}>
					<span class="flex items-center gap-1.5">
						<Dot tone={m.standby ? 'needs' : m.health === 'green' ? 'done' : m.health === 'red' ? 'fail' : 'idle'} />
						<span class="min-w-0 flex-1 truncate font-medium">{m.name}</span>
						<span class="num shrink-0 text-muted-foreground">{m.standby ? t('mStandbyShort') : t('mAgentsShort', m.busy, m.slots)}</span>
					</span>
					<span class="grid grid-cols-2 gap-1.5 pl-3">
						<span class="flex items-center gap-1"><span class="w-7 text-[9px] text-muted-foreground">CPU</span><span class="h-1 flex-1 rounded-full bg-muted"><span class="block h-1 rounded-full bg-st-work" style="width:{cpuPct(m)}%"></span></span></span>
						<span class="flex items-center gap-1"><span class="w-7 text-[9px] text-muted-foreground">RAM</span><span class="h-1 flex-1 rounded-full bg-muted"><span class="block h-1 rounded-full bg-st-review" style="width:{memPct(m)}%"></span></span></span>
					</span>
				</a>
			{/each}
			{#if machines.length > SHOWN}<a href={href('machines')} class="block px-1 text-[11px] text-muted-foreground hover:text-foreground">{t('mMore', machines.length - SHOWN)}</a>{/if}
		</div>
		<UserMenu />
	</Sidebar.Footer>
	<Sidebar.Rail />
</Sidebar.Root>
