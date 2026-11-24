<script lang="ts">
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Switch } from '$lib/components/ui/switch';
	import * as Select from '$lib/components/ui/select';
	import * as ToggleGroup from '$lib/components/ui/toggle-group';
	import * as Table from '$lib/components/ui/table';
	import PlusIcon from '@lucide/svelte/icons/plus';
	import KanbanIcon from '@lucide/svelte/icons/square-kanban';
	import ListIcon from '@lucide/svelte/icons/list';
	import SearchIcon from '@lucide/svelte/icons/search';
	import ChevronLeftIcon from '@lucide/svelte/icons/chevron-left';
	import ChevronRightIcon from '@lucide/svelte/icons/chevron-right';
	import XIcon from '@lucide/svelte/icons/x';
	import { store } from '$lib/api';
	import { t } from '$lib/i18n';
	import { compact, money, since } from '$lib/format';
	import { session } from '$lib/session.svelte';
	import { cluster } from '$lib/cluster.svelte';
	import { TEXT, BG, COLUMNS, COLUMN_TONE, toneOfItem, signalTone, signalText, type Column } from '$lib/status';
	import { clusterItems, type Row } from '$lib/app/items';
	import { newItem } from '$lib/app/actions';
	import { openItem } from '$lib/app/open';
	import PageHeader from '$lib/app/PageHeader.svelte';
	import ItemCard from '$lib/app/ItemCard.svelte';
	import StatusIcon from '$lib/app/StatusIcon.svelte';
	import Empty from '$lib/app/Empty.svelte';

	let view = $state(store.get('queueView') || 'board');
	let density = $state(store.get('density') || 'comfortable');
	let hideEmpty = $state(store.get('hideEmpty') === '1');
	let query = $state('');
	let prio = $state('all');
	let proj = $state('all');
	// On a phone the board shows one column at a time.
	let one = $state<Column>((store.get('queueColumn') as Column) || 'working');
	$effect(() => {
		store.set('queueView', view);
		store.set('density', density);
		store.set('hideEmpty', hideEmpty ? '1' : '');
		store.set('queueColumn', one);
	});

	const all = $derived(clusterItems(cluster.data));
	const rows = $derived(
		all.filter((r) => {
			if (prio !== 'all' && r.priority !== Number(prio)) return false;
			if (proj !== 'all' && r.project !== proj) return false;
			const q = query.trim().toLowerCase();
			return !q || (r.id + ' ' + r.title + ' ' + r.branch + ' ' + r.area + ' ' + r.project).toLowerCase().includes(q);
		})
	);
	const by = $derived.by(() => {
		const m = Object.fromEntries(COLUMNS.map((c) => [c, [] as Row[]])) as Record<Column, Row[]>;
		for (const r of rows) (m[(r.column || 'backlog') as Column] ||= []).push(r);
		for (const k of COLUMNS) m[k].sort((a, b) => a.priority - b.priority || a.id.localeCompare(b.id, undefined, { numeric: true }));
		return m;
	});
	const shown = $derived(COLUMNS.filter((c) => !hideEmpty || by[c].length));
	const filtered = $derived(query || prio !== 'all' || proj !== 'all');
	const projects = $derived([...new Set(all.map((r) => r.project))].sort());

	// The board scrolls sideways: the chips and arrows move it a column at a time.
	let board = $state<HTMLElement | null>(null);
	let atStart = $state(true);
	let atEnd = $state(false);
	function edges() {
		if (!board) return;
		atStart = board.scrollLeft < 8;
		atEnd = board.scrollLeft + board.clientWidth > board.scrollWidth - 8;
	}
	$effect(() => {
		void shown.length;
		void view;
		queueMicrotask(edges);
	});
	// The board fills the window down to the status bar, whatever the header
	// and filters above it take: its sideways scrollbar is always on screen.
	let height = $state(480);
	function fit() {
		if (!board) return;
		const bar = document.querySelector('footer')?.getBoundingClientRect().height || 28;
		const top = board.getBoundingClientRect().top;
		height = Math.max(260, Math.floor(window.innerHeight - top - bar - 12));
	}
	$effect(() => {
		if (!board) return;
		fit();
		const ro = new ResizeObserver(fit);
		ro.observe(document.getElementById('view')!);
		window.addEventListener('resize', fit);
		return () => {
			ro.disconnect();
			window.removeEventListener('resize', fit);
		};
	});
	const step = (dir: number) => board?.scrollBy({ left: dir * (board.clientWidth * 0.8), behavior: 'smooth' });
	function goTo(c: Column) {
		one = c;
		document.getElementById('col-' + c)?.scrollIntoView({ behavior: 'smooth', inline: 'start', block: 'nearest' });
	}
	// A vertical wheel over the column headers scrolls the board sideways.
	function wheel(e: WheelEvent) {
		if (!board || Math.abs(e.deltaX) > Math.abs(e.deltaY)) return;
		board.scrollLeft += e.deltaY;
		e.preventDefault();
	}
	function clear() {
		query = '';
		prio = 'all';
		proj = 'all';
	}
	const prioLabel = $derived(prio === 'all' ? t('allPriorities') : 'P' + prio);
</script>

<PageHeader title={t('queue')} icon={KanbanIcon} meta={[session.scope || t('allProjects'), t('queueCount', all.length), filtered ? t('shownN', rows.length) : '']}>
	{#snippet actions()}
		<ToggleGroup.Root type="single" bind:value={view} variant="outline" size="sm" class="shrink-0">
			<ToggleGroup.Item value="board" aria-label={t('viewBoard')} title={t('viewBoard')}><KanbanIcon /></ToggleGroup.Item>
			<ToggleGroup.Item value="table" aria-label={t('viewTable')} title={t('viewTable')}><ListIcon /></ToggleGroup.Item>
		</ToggleGroup.Root>
		<Button size="sm" onclick={newItem}><PlusIcon />{t('newItem')}</Button>
	{/snippet}
</PageHeader>

<!-- Filters: one line on a wide screen, wrapped on a narrow one. -->
<div class="mb-3 flex flex-wrap items-center gap-2">
	<div class="relative w-full sm:w-72">
		<SearchIcon class="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
		<Input bind:value={query} placeholder={t('queueSearch')} class="h-8 pl-8" aria-label={t('queueSearch')} />
	</div>
	<Select.Root type="single" bind:value={prio}>
		<Select.Trigger size="sm" class="h-8 w-auto min-w-28" aria-label={t('priority')}>{prioLabel}</Select.Trigger>
		<Select.Content>
			<Select.Item value="all" label={t('allPriorities')} />
			{#each [0, 1, 2, 3] as p (p)}<Select.Item value={String(p)} label={'P' + p} />{/each}
		</Select.Content>
	</Select.Root>
	{#if !session.scope && projects.length > 1}
		<Select.Root type="single" bind:value={proj}>
			<Select.Trigger size="sm" class="h-8 w-auto min-w-32" aria-label={t('project')}>{proj === 'all' ? t('allProjects') : proj}</Select.Trigger>
			<Select.Content>
				<Select.Item value="all" label={t('allProjects')} />
				{#each projects as p (p)}<Select.Item value={p} label={p} />{/each}
			</Select.Content>
		</Select.Root>
	{/if}
	{#if filtered}<Button size="sm" variant="ghost" class="h-8" onclick={clear}><XIcon />{t('clearFilters')}</Button>{/if}
	{#if view === 'board'}
		<div class="ml-auto flex items-center gap-3">
			<label class="hidden items-center gap-2 text-xs text-muted-foreground sm:flex"><Switch bind:checked={hideEmpty} class="scale-90" />{t('hideEmpty')}</label>
			<ToggleGroup.Root type="single" bind:value={density} variant="outline" size="sm" class="hidden shrink-0 sm:flex">
				<ToggleGroup.Item value="comfortable" class="px-2.5 text-xs">{t('comfortable')}</ToggleGroup.Item>
				<ToggleGroup.Item value="compact" class="px-2.5 text-xs">{t('compactView')}</ToggleGroup.Item>
			</ToggleGroup.Root>
		</div>
	{/if}
</div>

{#if !cluster.data}
	<Empty text={t('loading')} />
{:else if view === 'board'}
	<!-- Column chips: jump to a column (wide screens) or pick the one shown (phones). -->
	<div class="mb-3 flex items-center gap-2">
		<button class="hidden size-8 shrink-0 place-items-center rounded-md border bg-card hover:bg-muted disabled:opacity-30 md:grid" onclick={() => step(-1)} disabled={atStart} aria-label={t('scrollLeft')}><ChevronLeftIcon class="size-4" /></button>
		<div class="flex min-w-0 flex-1 gap-1.5 overflow-x-auto pb-1">
			{#each shown as c (c)}
				<button
					class="num flex shrink-0 items-center gap-1.5 rounded-full border px-2.5 py-1 text-[11px] whitespace-nowrap transition-colors hover:bg-muted
						{one === c ? 'max-md:border-foreground/40 max-md:bg-muted' : ''}"
					onclick={() => goTo(c)}
				>
					<span class="size-1.5 rounded-full {BG[COLUMN_TONE[c]]}"></span>{t('col_' + c)}<span class="text-muted-foreground">{by[c].length}</span>
				</button>
			{/each}
		</div>
		<button class="hidden size-8 shrink-0 place-items-center rounded-md border bg-card hover:bg-muted disabled:opacity-30 md:grid" onclick={() => step(1)} disabled={atEnd} aria-label={t('scrollRight')}><ChevronRightIcon class="size-4" /></button>
	</div>

	<!-- Phones: one column, full width, the page scrolls. -->
	<section class="space-y-2 md:hidden" aria-label={t('col_' + one)}>
		<h2 class="flex items-center gap-2 px-1"><span class="size-1.5 rounded-full {BG[COLUMN_TONE[one]]}"></span><span class="eyebrow font-medium {TEXT[COLUMN_TONE[one]]}">{t('col_' + one)}</span><span class="num ml-auto text-[11px] text-muted-foreground">{by[one].length}</span></h2>
		{#each by[one] as r (r.project + r.id)}<ItemCard {r} compact={density === 'compact'} />{:else}<p class="rounded-xl border border-dashed py-10 text-center text-sm text-muted-foreground">{t('colEmpty')}</p>{/each}
	</section>

	<!-- Wider screens: every column side by side, each scrolling on its own,
	     the board scrolling sideways with a scrollbar that stays visible. -->
	<div bind:this={board} onscroll={edges} class="scroll-x-visible hidden pb-2 md:block" style="height:{height}px" role="region" aria-label={t('queue')}>
		<div class="flex h-full min-w-full gap-3">
			{#each shown as c (c)}
				<section id={'col-' + c} class="flex min-w-[18.5rem] flex-1 basis-0 scroll-ml-1 flex-col overflow-hidden rounded-xl border bg-surface-1/60 xl:min-w-[19.5rem]" aria-label={t('col_' + c)}>
					<header class="flex shrink-0 items-center gap-2 border-b bg-card/60 px-3 py-2.5" onwheel={wheel}>
						<span class="size-1.5 rounded-full {BG[COLUMN_TONE[c]]}"></span>
						<span class="eyebrow font-medium {TEXT[COLUMN_TONE[c]]}">{t('col_' + c)}</span>
						<span class="num ml-auto rounded-full bg-muted px-1.5 text-[11px] text-muted-foreground">{by[c].length}</span>
					</header>
					<div class="min-h-0 flex-1 space-y-2 overflow-y-auto p-2">
						{#each by[c] as r (r.project + r.id)}<ItemCard {r} compact={density === 'compact'} />{:else}<p class="px-1 py-10 text-center text-xs text-muted-foreground">{t('colEmpty')}</p>{/each}
					</div>
				</section>
			{/each}
		</div>
	</div>
{:else}
	<!-- Table: a list on phones, a table with every column from tablets up. -->
	<div class="divide-y overflow-hidden rounded-xl border bg-card md:hidden">
		{#each rows as r (r.project + r.id)}
			{@const tone = toneOfItem(r)}
			{@const sub = { text: signalText(r), tone: signalTone(r) }}
			<button class="flex w-full items-start gap-3 px-4 py-3 text-left hover:bg-muted/40" onclick={() => openItem(r.project, r.id)}>
				<StatusIcon {tone} spin={r.activity === 'working'} class="mt-0.5" />
				<span class="min-w-0 flex-1">
					<span class="line-clamp-2 text-sm font-medium">{r.title}</span>
					<span class="num mt-0.5 block truncate text-[11px] {TEXT[sub.tone]}">{sub.text}</span>
					<span class="num mt-0.5 flex flex-wrap gap-x-3 text-[11px] text-muted-foreground"><span>{r.id}</span>{#if !session.scope}<span>{r.project}</span>{/if}<span>{r.step}</span>{#if r.usage}<span>{money(r.usage.costUsd)}</span>{/if}<span>{since(r.updatedAt)}</span></span>
				</span>
			</button>
		{:else}<div class="p-4"><Empty /></div>{/each}
	</div>
	<div class="scroll-x-visible hidden rounded-xl border bg-card md:block">
		<Table.Root class="min-w-[56rem]">
			<Table.Header class="sticky top-0 bg-card"><Table.Row class="eyebrow">
				<Table.Head class="w-[38%]">{t('item')}</Table.Head><Table.Head>{t('ovActivity')}</Table.Head><Table.Head>{t('step')}</Table.Head><Table.Head>{t('machine')}</Table.Head>
				<Table.Head>PR</Table.Head><Table.Head class="text-right">{t('tokens')}</Table.Head><Table.Head class="text-right">{t('ovCost')}</Table.Head><Table.Head class="text-right">{t('updated')}</Table.Head>
			</Table.Row></Table.Header>
			<Table.Body>
				{#each rows as r (r.project + r.id)}
					{@const tone = toneOfItem(r)}
					{@const sub = { text: signalText(r), tone: signalTone(r) }}
					<Table.Row class="cursor-pointer" onclick={() => openItem(r.project, r.id)}>
						<Table.Cell class="max-w-0"><div class="flex items-start gap-2"><StatusIcon {tone} spin={r.activity === 'working'} class="mt-0.5" /><div class="min-w-0"><p class="truncate font-medium" title={r.title}>{r.title}</p><p class="num text-[11px] text-muted-foreground">{r.id}{session.scope ? '' : ' · ' + r.project}</p></div></div></Table.Cell>
						<Table.Cell><p class="num max-w-56 truncate text-xs {TEXT[sub.tone]}" title={sub.text}>{sub.text}</p></Table.Cell>
						<Table.Cell class="num text-xs">{r.step}</Table.Cell>
						<Table.Cell class="num text-xs">{r.machine}</Table.Cell>
						<Table.Cell class="num text-xs whitespace-nowrap">{#if r.pr?.number}#{r.pr.number} · {r.pr.state}{/if}</Table.Cell>
						<Table.Cell class="num text-right text-xs">{compact(r.usage?.tokens)}</Table.Cell>
						<Table.Cell class="num text-right text-xs">{money(r.usage?.costUsd)}</Table.Cell>
						<Table.Cell class="num text-right text-xs whitespace-nowrap text-muted-foreground">{since(r.updatedAt)}</Table.Cell>
					</Table.Row>
				{:else}<Table.Row><Table.Cell colspan={8}><Empty /></Table.Cell></Table.Row>{/each}
			</Table.Body>
		</Table.Root>
	</div>
{/if}
