<script lang="ts">
	import ArrowLeftIcon from '@lucide/svelte/icons/arrow-left';
	import ArrowRightIcon from '@lucide/svelte/icons/arrow-right';
	import GitCommitIcon from '@lucide/svelte/icons/git-commit-horizontal';
	import ScaleIcon from '@lucide/svelte/icons/scale';
	import LightbulbIcon from '@lucide/svelte/icons/lightbulb';
	import MapPinIcon from '@lucide/svelte/icons/map-pin';
	import FileTextIcon from '@lucide/svelte/icons/file-text';
	import TicketIcon from '@lucide/svelte/icons/ticket';
	import { t } from '$lib/i18n';
	import { cluster } from '$lib/cluster.svelte';
	import { TEXT, toneOfItem, signalText, signalTone } from '$lib/status';
	import { clusterItems, type Row } from './items';
	import { openItem } from './open';
	import StatusIcon from './StatusIcon.svelte';
	import Empty from './Empty.svelte';

	// data: what the daemon's Related answers for the item (a loose object).
	let { data, project, id }: { data: Record<string, any>; project: string; id: string } = $props();

	const rows = $derived(new Map(clusterItems(cluster.data).filter((r) => r.project === project).map((r) => [r.id, r])));
	const ITEM = /^I-\d+$/i;
	// The daemon gives items as "I-20 Title (status)": the id is the first word.
	const idOf = (s: string) => String(s).trim().split(/\s+/)[0].toUpperCase();
	const restOf = (s: string) => String(s).trim().split(/\s+/).slice(1).join(' ').replace(/\s*\((\w+)\)$/, '');
	const list = (v: unknown): string[] => (Array.isArray(v) ? v.map(String) : []);

	type Ref = { id: string; title: string; row?: Row };
	const ref = (s: string): Ref => {
		const i = idOf(s);
		return { id: i, title: rows.get(i)?.title || restOf(s), row: rows.get(i) };
	};
	const waitsOn = $derived(list(data.depends_on ?? data.dependson).map(ref));
	const waitedBy = $derived(list(data.dependents).map(ref));
	const sameArea = $derived(list(data.same_area).map(ref));
	const open = (r?: Row) => !!r && r.status !== 'integrated' && r.status !== 'closed';

	// Links of the knowledge base, apart from the dependencies shown above.
	type Link = { from: string; to: string; kind: string };
	const links = $derived(
		((data.links || []) as Link[])
			.map((l) => ({ ...l, other: l.from.toUpperCase() === id.toUpperCase() ? l.to : l.from }))
			.filter((l) => !/^depend/.test(l.kind))
	);
	const kindOf = (other: string) => {
		const p = other.split('-')[0].toUpperCase();
		return p === 'D' ? 'decision' : p === 'P' ? 'proposal' : p === 'A' ? 'area' : p === 'I' ? 'item' : 'doc';
	};
	const linkIcon = { decision: ScaleIcon, proposal: LightbulbIcon, area: MapPinIcon, item: ArrowRightIcon, doc: FileTextIcon } as const;
	const otherName = (o: string) => (kindOf(o) === 'area' ? o.slice(2) : o);

	const anchors = $derived((data.anchors || {}) as Record<string, any>);
	const ANCHORS = ['files', 'symbols', 'areas', 'screens', 'contracts', 'decisions', 'specs'];
	const anchorKeys = $derived(ANCHORS.filter((k) => list(anchors[k]).length));
	const commits = $derived(
		list(data.commits).map((c) => {
			const [sha, ...rest] = c.split(' ');
			return { sha, subject: rest.join(' ') };
		})
	);
	const nothing = $derived(!waitsOn.length && !waitedBy.length && !sameArea.length && !links.length && !anchorKeys.length && !commits.length);
</script>

{#snippet item(r: Ref)}
	{@const row = r.row}
	<button class="flex w-full items-center gap-2.5 rounded-lg border bg-card px-3 py-2 text-left transition-colors hover:bg-muted/50" onclick={() => ITEM.test(r.id) && openItem(project, r.id)}>
		<StatusIcon tone={row ? toneOfItem(row) : 'done'} spin={row?.activity === 'working'} />
		<span class="num shrink-0 text-xs font-semibold">{r.id}</span>
		<span class="min-w-0 flex-1 truncate text-sm" title={r.title}>{r.title}</span>
		{#if row}<span class="num shrink-0 truncate text-[11px] {TEXT[signalTone(row)]}" style="max-width:14rem">{signalText(row)}</span>{/if}
	</button>
{/snippet}

{#snippet group(title: string, sub: string, refs: Ref[], icon: typeof ArrowLeftIcon)}
	{@const Icon = icon}
	<div class="space-y-2">
		<p class="flex items-center gap-1.5 text-xs font-medium"><Icon class="size-3.5 text-muted-foreground" />{title}<span class="num font-normal text-muted-foreground">{refs.length}</span></p>
		{#if sub}<p class="text-xs text-muted-foreground">{sub}</p>{/if}
		<div class="space-y-1.5">{#each refs as r (r.id)}{@render item(r)}{/each}</div>
	</div>
{/snippet}

{#if nothing}
	<Empty text={t('relNothing')} />
{:else}
	<div class="space-y-5">
		{#if waitsOn.length || waitedBy.length}
			<div class="grid gap-5 lg:grid-cols-2">
				{#if waitsOn.length}
					{@render group(t('relWaitsOn'), waitsOn.some((r) => open(r.row)) ? t('relWaitsOnOpen') : t('relWaitsOnDone'), waitsOn, ArrowLeftIcon)}
				{/if}
				{#if waitedBy.length}{@render group(t('relWaitedBy'), t('relWaitedByHelp'), waitedBy, ArrowRightIcon)}{/if}
			</div>
		{/if}

		{#if links.length}
			<div class="space-y-2">
				<p class="text-xs font-medium">{t('relLinks')}</p>
				<div class="flex flex-wrap gap-1.5">
					{#each links as l (l.from + l.to + l.kind)}
						{@const k = kindOf(l.other)}
						{@const Icon = linkIcon[k]}
						<button class="flex items-center gap-1.5 rounded-full border bg-card px-2.5 py-1 text-xs hover:bg-muted/50 disabled:cursor-default disabled:hover:bg-card"
							disabled={k !== 'item'} onclick={() => openItem(project, l.other)} title={t('relKind_' + k)}>
							<Icon class="size-3.5 text-muted-foreground" /><span class="text-muted-foreground">{t('relKind_' + k)}</span><span class="num font-medium">{otherName(l.other)}</span>
						</button>
					{/each}
				</div>
			</div>
		{/if}

		{#if anchorKeys.length || anchors.base}
			<div class="space-y-2">
				<p class="text-xs font-medium">{t('anchors')}</p>
				{#each anchorKeys as k (k)}
					<div class="flex flex-wrap items-baseline gap-1.5"><span class="eyebrow w-40 shrink-0 whitespace-nowrap text-muted-foreground">{t(k)}</span>{#each list(anchors[k]) as a (a)}<code class="num rounded bg-muted px-1.5 py-0.5 text-[11px]">{a}</code>{/each}</div>
				{/each}
				{#if anchors.base}<div class="flex items-baseline gap-1.5"><span class="eyebrow w-40 shrink-0 whitespace-nowrap text-muted-foreground">{t('base')}</span><code class="num rounded bg-muted px-1.5 py-0.5 text-[11px]" title={anchors.base}>{String(anchors.base).slice(0, 8)}</code></div>{/if}
			</div>
		{/if}

		{#if commits.length}
			<div class="space-y-2">
				<p class="flex items-center gap-1.5 text-xs font-medium"><GitCommitIcon class="size-3.5 text-muted-foreground" />{t('relCommits')}<span class="num font-normal text-muted-foreground">{commits.length}</span></p>
				<ol class="divide-y overflow-hidden rounded-lg border">
					{#each commits as c (c.sha)}
						<li class="flex items-baseline gap-3 px-3 py-1.5 text-sm"><code class="num shrink-0 text-[11px] text-muted-foreground">{c.sha.slice(0, 7)}</code><span class="min-w-0 truncate" title={c.subject}>{c.subject}</span></li>
					{/each}
				</ol>
			</div>
		{/if}

		{#if sameArea.length}
			{@render group(t('relSameArea'), '', sameArea, MapPinIcon)}
		{/if}

		{#if data.tracker || data.pr}
			<p class="flex flex-wrap items-center gap-3 text-xs text-muted-foreground">
				{#if data.tracker}<span class="flex items-center gap-1"><TicketIcon class="size-3.5" />{t('relTracker', data.tracker)}</span>{/if}
				{#if data.pr}<span>PR {data.pr}</span>{/if}
			</p>
		{/if}
	</div>
{/if}
