<script lang="ts">
	import * as Tabs from '$lib/components/ui/tabs';
	import { Button } from '$lib/components/ui/button';
	import { Spinner } from '$lib/components/ui/spinner';
	import { session } from '$lib/session.svelte';
	import { Badge } from '$lib/components/ui/badge';
	import CheckIcon from '@lucide/svelte/icons/circle-check';
	import CircleIcon from '@lucide/svelte/icons/circle-dashed';
	import GitBranchIcon from '@lucide/svelte/icons/git-branch';
	import GitPullRequestIcon from '@lucide/svelte/icons/git-pull-request';
	import ExternalLinkIcon from '@lucide/svelte/icons/external-link';
	import TicketIcon from '@lucide/svelte/icons/ticket';
	import GitCompareIcon from '@lucide/svelte/icons/git-compare';
	import HandIcon from '@lucide/svelte/icons/hand';
	import UndoIcon from '@lucide/svelte/icons/undo-2';
	import MessagesIcon from '@lucide/svelte/icons/messages-square';
	import { discussion } from '$lib/app/discussion.svelte';
	import { api } from '$lib/api';
	import { t } from '$lib/i18n';
	import { compact, dur, money, num, since, when, bytes } from '$lib/format';
	import { loader } from '$lib/loader.svelte';
	import { TEXT, toneOfItem, signalTone, signalText } from '$lib/status';
	import type { Row } from '$lib/app/items';
	import { act } from '$lib/app/act';
	import { dialog } from '$lib/app/dialog.svelte';
	import Loading from '$lib/app/Loading.svelte';
	import StatusBadge from '$lib/app/StatusBadge.svelte';
	import StatusIcon from '$lib/app/StatusIcon.svelte';
	import KV from '$lib/app/KV.svelte';
	import Section from '$lib/app/Section.svelte';
	import Stepper from '$lib/app/Stepper.svelte';
	import ApprovalButtons from '$lib/app/ApprovalButtons.svelte';
	import Timeline from '$lib/app/Timeline.svelte';
	import Empty from '$lib/app/Empty.svelte';
	import Markdown from '$lib/app/Markdown.svelte';
	import ItemDocuments from '$lib/app/ItemDocuments.svelte';
	import ItemImages from '$lib/app/ItemImages.svelte';
	import ItemWorkCard from '$lib/app/ItemWork.svelte';
	import { work as workApi, type ItemWork } from '$lib/rest';
	import AgentLive from '$lib/app/AgentLive.svelte';
	import QuestionCard from '$lib/app/QuestionCard.svelte';
	import * as DropdownMenu from '$lib/components/ui/dropdown-menu';
	import PencilIcon from '@lucide/svelte/icons/pencil';
	import SendIcon from '@lucide/svelte/icons/send';
	import EllipsisIcon from '@lucide/svelte/icons/ellipsis';
	import FileTextIcon from '@lucide/svelte/icons/file-text';
	import BotIcon from '@lucide/svelte/icons/bot';
	import ActivityIcon from '@lucide/svelte/icons/activity';
	import ListChecksIcon from '@lucide/svelte/icons/list-checks';
	import NetworkIcon from '@lucide/svelte/icons/network';
	import CpuIcon from '@lucide/svelte/icons/cpu';
	import CoinsIcon from '@lucide/svelte/icons/coins';
	import FileStackIcon from '@lucide/svelte/icons/file-stack';
	import Link2Icon from '@lucide/svelte/icons/link-2';
	import ChevronDownIcon from '@lucide/svelte/icons/chevron-down';
	import LockOpenIcon from '@lucide/svelte/icons/lock-open';
	import ArchiveXIcon from '@lucide/svelte/icons/archive-x';
	import WandIcon from '@lucide/svelte/icons/wand-sparkles';
	import Bars from '$lib/app/Bars.svelte';
	import ProcSummary from '$lib/app/ProcSummary.svelte';
	import ProcTable from '$lib/app/ProcTable.svelte';
	import WorktreeList from '$lib/app/WorktreeList.svelte';
	import type { ProcRow } from '$lib/procs.svelte';
	import type { TimelineEntry, UsageTotals } from '$lib/gen/barnraise/v1/barnraise_pb';
	import type { Change } from '$lib/session.svelte';
	import { editItem, closeItem, unlockFiles, addTweak, messageAgent } from '$lib/app/ownerActions';
	import { onDestroy } from 'svelte';
	import HourglassIcon from '@lucide/svelte/icons/hourglass';
	import DepGraph from '$lib/app/DepGraph.svelte';
	import Related from '$lib/app/Related.svelte';
	import { cluster } from '$lib/cluster.svelte';
	import { clusterItems } from '$lib/app/items';
	import { openItem } from '$lib/app/open';

	let { id, project }: { id: string; project: string } = $props();
	// Events about other items, the agents' heartbeats, accounts and machines
	// change nothing on this page: it reads again for its own item, for what
	// can unblock it (another item's step) and for the owner's questions.
	// What its machines run is read every few seconds below, while it runs.
	const QUIET = /^(agent\.|account\.|machine\.|message\.|journal\.|policy\.|secret\.|owner\.discussion|autopilot\.)/;
	const relevant = (changes: Change[]) => changes.some((c) => c.project === project && (c.subject === id || !QUIET.test(c.type)));
	let lastEntries: TimelineEntry[] = [];
	const data = loader(async () => {
		const finishedNow = (s?: string) => s === 'integrated' || s === 'closed';
		const item = api.getItem({ project, id });
		const [ir, tl, est, qs, ov] = await Promise.all([
			item,
			api.timeline({ project, id }).catch(() => null),
			// The estimate is shown only before the item is done.
			item.then((r) => (finishedNow(r.item?.status) ? null : api.estimate({ project, id }))).catch(() => null),
			api.listQuestions({ project }).catch(() => null),
			api.overview({ project, item: id, all: true }).catch(() => null)
		]);
		// The open questions about this item, answered right here.
		const questions = (qs?.questions || []).filter((q) => q.itemId === id && !q.answer);
		// The board's facts of this item (flow, what it awaits, signal, checks), from the daemon.
		const row = ov?.projects[0]?.items.find((r) => r.id === id);
		// The same timeline again keeps its entries, so nothing of it is drawn again.
		let entries = tl?.entries || [];
		if (entries.length === lastEntries.length && entries.at(-1)?.seq === lastEntries.at(-1)?.seq) entries = lastEntries;
		lastEntries = entries;
		return { it: ir.item!, entries, est, questions, row: row ? ({ ...row, project } as Row) : undefined };
	}, { relevant });
	let tab = $state('summary');
	// Underlined tabs, sized to the bar; the count beside a label is quieter than it.
	const tabCls =
		'h-10 flex-none rounded-none px-2.5 text-muted-foreground data-active:text-foreground data-active:!shadow-[inset_0_-2px_0_0_currentColor]';
	const countCls = 'num rounded-full bg-muted px-1.5 text-[10.5px] font-medium text-muted-foreground';
	// The checks live on the criteria tab, the proof next to what it proves.
	$effect(() => {
		if (tab === 'checks') tab = 'criteria';
	});
	// What the item's machines run changes in seconds: while something runs
	// for it, read it again every few seconds.
	// The proof of the item's work (its commits and pull request), read again
	// whenever the timeline grows: a delivery is a new commit.
	let work = $state<ItemWork | null>(null);
	const timelineSize = $derived(data.data?.entries.length ?? 0);
	$effect(() => {
		void timelineSize;
		workApi
			.ofItem(project, id)
			.then((v) => (work = v))
			.catch(() => (work = null));
	});
	const commitUrls = $derived(Object.fromEntries((work?.steps || []).filter((s) => s.commit && s.url).map((s) => [s.commit!, s.url!])));
	const live = $derived(!!(data.data?.row && (data.data.row.sessions.length || data.data.row.procs?.count)));
	const timer = window.setInterval(() => {
		if (live && !document.hidden) data.reload();
	}, 5_000);
	onDestroy(() => clearInterval(timer));
	// The knowledge base's links of the item (decisions, files, other items), read when its tab opens.
	let related = $state<{ data?: Record<string, any>; error?: string } | null>(null);
	$effect(() => {
		if (tab === 'related' && !related) api.related({ project, target: id }).then((r) => (related = { data: (r.result as Record<string, any>) || {} })).catch((e) => (related = { error: String(e) }));
	});
	const finished = (s: string) => s === 'integrated' || s === 'closed';
	// The cluster's items, with this one as the item page knows it, for its dependency chain.
	const depRows = $derived.by(() => {
		const rows = clusterItems(cluster.data).filter((r) => r.project === project);
		const mine = data.data?.row;
		return mine ? [...rows.filter((r) => r.id !== id), mine] : rows;
	});
	const cost = (m: Record<string, UsageTotals> | undefined) => Object.fromEntries(Object.entries(m || {}).map(([k, u]) => [k, u.costUsd]));
	const procRowsOf = (r: Row | undefined): ProcRow[] =>
		(r?.worktrees || []).flatMap((w) => (w.procs?.top || []).map((p) => ({ ...p, machine: w.machine, project: w.project, worktree: w.name, wkind: w.kind, itemId: w.itemId, path: w.path }) as ProcRow));
	const verdict = (x: string) => (x === 'pass' ? t('passed') : x === 'fail' ? t('failed') : x || '—');

	const takeover = () => act(() => api.takeover({ project, id }), (r) => t('takenOver', r.worktree || ''));
	function handback() {
		dialog.open({
			title: t('handbackTitle', id),
			fields: [{ name: 'note', label: t('note'), type: 'textarea' }],
			submit: t('handback'),
			onSubmit: async (v) => {
				await api.handback({ project, id, note: v.note });
				return t('handedBack');
			}
		});
	}
	const ANCHORS = ['files', 'symbols', 'areas', 'screens', 'contracts', 'decisions', 'specs'] as const;
</script>

<Loading state={data}>
	{@const { it, entries, est, questions, row } = data.data!}
	{@const tone = row ? toneOfItem(row) : 'idle'}
	{@const sub = row ? { text: signalText(row), tone: signalTone(row) } : null}
	{@const links = row?.links}
	{@const holder = it.claim && (it.claim.agent || it.claim.machine) ? [it.claim.agent, it.claim.machine].filter(Boolean).join(' @ ') : ''}
	{(document.title = it.id + ' · ' + it.title, '')}

	<header class="mb-5 space-y-4">
		<div class="flex flex-wrap items-start justify-between gap-4">
			<div class="min-w-0 space-y-2">
				<div class="num flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
					<span>{it.id}</span><span>·</span><span>{project}</span>
					{#if it.kind}<span>·</span><span>{it.kind}</span>{/if}
					{#if it.area}<span>·</span><span>{it.area}</span>{/if}
				</div>
				<h1 class="flex items-start gap-2.5 text-xl font-semibold leading-tight tracking-tight"><StatusIcon {tone} size={18} spin={row?.activity === 'working'} class="mt-1" />{it.title}</h1>
				<div class="flex flex-wrap items-center gap-1.5">
					{#if row}<StatusBadge status={row.activity || it.status} {tone} />{/if}
					<Badge variant={it.priority <= 1 ? 'destructive' : 'secondary'} class="num">P{it.priority}</Badge>
					{#if it.size && it.size !== 'unknown'}<Badge variant="outline">{it.size}</Badge>{/if}
					{#if it.risky}<Badge variant="destructive">{t('risky')}</Badge>{/if}
					{#if it.parked}<Badge variant="secondary">{t('parked')}</Badge>{/if}
					{#if sub}<span class="num ml-1 text-xs {TEXT[sub.tone]}">{sub.text}</span>{/if}
				</div>
			</div>
			<div class="flex flex-wrap gap-2">
				{#if row?.awaits}<ApprovalButtons id={it.id} awaits={row.awaits} {project} />{/if}
				{#if session.autopilotWorking[project + '/' + it.id]}<span class="inline-flex items-center gap-1.5 text-sm text-st-review" role="status"><Spinner class="size-3.5" />{t('autopilotDeciding')}</span>{/if}
				<Button size="sm" variant="secondary" onclick={() => discussion.show(project, it.id, it.title)}><MessagesIcon />{t('discuss')}</Button>
				{#if !finished(it.status)}
					{#if it.claim?.agent || row?.sessions.length}<Button size="sm" variant="outline" onclick={() => messageAgent(project, it.id)}><SendIcon />{t('msgShort')}</Button>{/if}
					<Button size="sm" variant="outline" onclick={() => editItem(project, it, data.reload)}><PencilIcon />{t('edit')}</Button>
				{/if}
				{#if it.status === 'paused'}<Button size="sm" variant="outline" onclick={handback}><UndoIcon />{t('handback')}</Button>
				{:else if !finished(it.status)}<Button size="sm" variant="outline" onclick={takeover}><HandIcon />{t('takeover')}</Button>{/if}
				{#if !finished(it.status)}
					<DropdownMenu.Root>
						<DropdownMenu.Trigger>{#snippet child({ props })}<Button size="icon" variant="outline" class="size-8" aria-label={t('moreActions')} {...props}><EllipsisIcon /></Button>{/snippet}</DropdownMenu.Trigger>
						<DropdownMenu.Content align="end" class="w-56">
							<DropdownMenu.Item onSelect={() => addTweak(project, it.area)}><WandIcon />{t('tweakTitle')}</DropdownMenu.Item>
							<DropdownMenu.Item onSelect={() => unlockFiles(project, it)}><LockOpenIcon />{t('unlockShort')}</DropdownMenu.Item>
							<DropdownMenu.Separator />
							<DropdownMenu.Item variant="destructive" onSelect={() => closeItem(project, it)}><ArchiveXIcon />{t('closeShort')}</DropdownMenu.Item>
						</DropdownMenu.Content>
					</DropdownMenu.Root>
				{/if}
			</div>
		</div>
		<Stepper flow={row?.flow || []} {tone} />
		{#if it.status === 'closed'}<div class="rounded-lg border border-l-2 border-l-st-idle bg-muted/40 p-3 text-sm">{t('closedLine', t('close_' + it.closedReason), it.closedBy || '—')}</div>{/if}
		{#if finished(it.status) && !it.criteria.length}<div class="rounded-lg border border-l-2 border-l-st-needs bg-st-needs/5 p-3 text-sm">{t('integratedNoCriteria')}</div>{/if}
		{#if row && !finished(it.status) && row.blockedBy?.length}
			<div class="flex flex-wrap items-center gap-2 rounded-lg border border-l-2 border-l-st-needs bg-st-needs/5 p-3 text-sm">
				<HourglassIcon class="size-4 shrink-0 text-st-needs" />
				<span>{t('depBanner')}</span>
				{#each row.blockedBy as d (d)}<button class="num rounded-full border bg-card px-2 py-0.5 text-xs font-medium hover:bg-muted" onclick={() => openItem(project, d)}>{d}</button>{/each}
				<span class="text-muted-foreground">{t('depBannerAfter')}</span>
			</div>
		{/if}
		{#each questions as q (q.id)}<QuestionCard {q} {project} />{:else}{#if it.waitingQuestion}<div class="rounded-lg border border-l-2 border-l-st-needs bg-st-needs/5 p-3 text-sm">{t('waitingQuestion', it.waitingQuestion)}</div>{/if}{/each}
	</header>

	<div class="grid gap-4 xl:grid-cols-[minmax(0,1fr)_22rem]">
		<Tabs.Root bind:value={tab} class="min-w-0">
			{@const checksOk = row?.checks.filter((c) => c.passed).length || 0}
			{@const checksN = row?.checks.length || 0}
			{@const hasDeps = it.deps.length > 0 || depRows.some((r) => r.deps?.includes(id))}
			{@const showAgent = !finished(it.status) || !!row?.sessions.length}
			{@const more = [
				{ v: 'resources', label: t('tabResources'), icon: CpuIcon, show: !!(row?.worktrees.length || row?.procs?.count || row?.sessions.length), count: row?.procs?.count ? String(row.procs.count) : '' },
				{ v: 'cost', label: t('ovCost'), icon: CoinsIcon, show: !!(row?.usage && Number(row.usage.tokens)), count: row?.usage && Number(row.usage.tokens) ? money(row.usage.costUsd) : '' },
				{ v: 'documents', label: t('tabDocuments'), icon: FileStackIcon, show: true, count: '' },
				{ v: 'related', label: t('tabRelated'), icon: Link2Icon, show: true, count: '' }
			].filter((m) => m.show)}
			{@const inMore = more.find((m) => m.v === tab)}
			<!-- Five tabs at most in the bar, the ones looked at while an item moves;
			     what is read now and then sits behind "More", which takes the name of
			     the tab open from it. -->
			<div class="relative flex items-center gap-1 border-b">
				<Tabs.List variant="line" class="!h-10 min-w-0 justify-start gap-0.5 overflow-x-auto overflow-y-hidden p-0 [scrollbar-width:none]">
					<Tabs.Trigger value="summary" class={tabCls}><FileTextIcon />{t('tabSummary')}</Tabs.Trigger>
					{#if showAgent}
						<Tabs.Trigger value="agent" class={tabCls}>
							<BotIcon />{t('tabAgent')}
							{#if row?.sessions.length}<span class="relative ml-0.5 flex size-2" aria-label={t('live')}><span class="absolute inline-flex size-full animate-ping rounded-full bg-st-work opacity-60"></span><span class="relative inline-flex size-2 rounded-full bg-st-work"></span></span>{/if}
						</Tabs.Trigger>
					{/if}
					<Tabs.Trigger value="timeline" class={tabCls}><ActivityIcon />{t('timeline')}<span class={countCls}>{entries.length}</span></Tabs.Trigger>
					<Tabs.Trigger value="criteria" class={tabCls}>
						<ListChecksIcon />{t('criteria')}
						<span class={countCls}>{it.criteria.filter((c) => c.met).length}/{it.criteria.length}</span>
						{#if checksN}<span class="num rounded-full px-1.5 text-[10.5px] font-medium {checksOk === checksN ? 'bg-st-done/12 text-st-done' : 'bg-st-fail/12 text-st-fail'}" title={t('tabChecks')}>{checksOk === checksN ? '✓' : '✕'} {checksOk}/{checksN}</span>{/if}
					</Tabs.Trigger>
					{#if hasDeps}<Tabs.Trigger value="deps" class={tabCls}><NetworkIcon />{t('tabDeps')}</Tabs.Trigger>{/if}
				</Tabs.List>
				<DropdownMenu.Root>
					<DropdownMenu.Trigger>
						{#snippet child({ props })}
							<button {...props} class="relative inline-flex h-10 shrink-0 items-center gap-1.5 px-2.5 text-sm font-medium transition-colors hover:text-foreground {inMore ? 'text-foreground shadow-[inset_0_-2px_0_0_currentColor]' : 'text-muted-foreground'}">
								{#if inMore}{@const Icon = inMore.icon}<Icon class="size-4" />{inMore.label}{:else}{t('tabMore')}{/if}
								<ChevronDownIcon class="size-3.5 opacity-60" />
							</button>
						{/snippet}
					</DropdownMenu.Trigger>
					<DropdownMenu.Content align="start" class="w-56">
						{#each more as m (m.v)}
							{@const Icon = m.icon}
							<DropdownMenu.Item onSelect={() => (tab = m.v)} class={tab === m.v ? 'bg-muted' : ''}>
								<Icon />{m.label}{#if m.count}<span class="num ml-auto text-xs text-muted-foreground">{m.count}</span>{/if}
							</DropdownMenu.Item>
						{/each}
					</DropdownMenu.Content>
				</DropdownMenu.Root>
			</div>
			<Tabs.Content value="summary" class="mt-3 space-y-4">
				<ItemImages {project} id={it.id} version={it.step + '/' + (it.mergeCommit || '') + '/' + (it.runs ? Object.keys(it.runs).length : 0)} />
				{#if work && (work.pr?.number || work.commits.length || work.merged)}<ItemWorkCard {work} />{/if}
				{#if it.body}<Section title={t('body')}><Markdown text={it.body} /></Section>{/if}
				{#each [['preserve', it.preserve], ['checklist', it.checklist], ['clarifications', it.clarifications], ['tweaks', it.tweaks]] as [k, arr] (k)}
					{#if (arr as string[]).length}<Section title={t(k as string)}><ul class="list-disc space-y-1 pl-5 text-sm">{#each arr as string[] as x, i (i)}<li>{x}</li>{/each}</ul></Section>{/if}
				{/each}
				{#if it.rework.length}
					<Section title={t('rework')} count={it.rework.length}>
						<ol class="space-y-2 text-sm">{#each it.rework as r, i (i)}<li class="flex gap-2"><span class="num text-xs text-muted-foreground">#{i + 1}</span><span>{t('reworkLine', r.from, r.to, r.by, r.reason)}{#if r.outcome}<span class="text-muted-foreground"> · {r.outcome}</span>{/if}<span class="num block text-[11px] text-muted-foreground">{when(r.at)}</span></span></li>{/each}</ol>
					</Section>
				{/if}
				{#if it.anchors && (ANCHORS.some((k) => it.anchors![k].length) || it.anchors.base)}
					{@const an = it.anchors}
					<Section title={t('anchors')}>
						<div class="space-y-2">{#each ANCHORS.filter((k) => an[k].length) as k (k)}<div><p class="eyebrow mb-1 text-muted-foreground">{t(k)}</p><div class="flex flex-wrap gap-1">{#each an[k] as a (a)}<code class="num rounded bg-muted px-1.5 py-0.5 text-[11px]">{a}</code>{/each}</div></div>{/each}</div>
					</Section>
				{/if}
				{#if !it.body && !it.rework.length}<Empty />{/if}
			</Tabs.Content>
			<Tabs.Content value="deps" class="mt-3">{#if tab === 'deps'}<Section title={t('tabDeps')}><DepGraph rows={depRows} focus={project + '/' + id} compact /></Section>{/if}</Tabs.Content>
			<Tabs.Content value="timeline" class="mt-3">{#if tab === 'timeline'}<Section title={t('timeline')} count={entries.length}><Timeline item={it} {entries} {project} {commitUrls} /></Section>{/if}</Tabs.Content>
			<Tabs.Content value="criteria" class="mt-3">{#if tab === 'criteria'}
				<Section title={t('criteria')} count={it.criteria.filter((c) => c.met).length + '/' + it.criteria.length} pad={false}>
					{#if !it.criteria.length}<div class="p-4"><Empty /></div>{:else}
						<ul class="divide-y">
							{#each it.criteria as c, i (i)}
								<li class="flex gap-3 px-4 py-3 text-sm">
									{#if c.met}<CheckIcon class="mt-0.5 size-4 shrink-0 text-st-done" aria-label={t('met')} />{:else}<CircleIcon class="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-label={t('notMet')} />{/if}
									<div class="min-w-0 space-y-1">
										<p>{#if c.id}<span class="num mr-1.5 text-[11px] text-muted-foreground">{c.id}</span>{/if}{c.text}</p>
										{#if c.check}<p class="num text-[11px] text-muted-foreground">{t('checkName', c.check)}</p>{/if}
										{#if c.evidence}<p class="text-xs text-st-done">{t('evidence', c.evidence)}</p>{:else if c.met}<p class="text-xs text-st-needs">{t('noEvidence')}</p>{/if}
									</div>
								</li>
							{/each}
						</ul>
					{/if}
				</Section>
				{#if row?.checks.length}
					<div class="mt-4">
					<Section title={t('tabChecks')} pad={false}>
						<ul class="divide-y">
							{#each row.checks as c (c.check + c.at)}
								<li class="num flex items-center gap-3 px-4 py-2.5 text-xs">
									<StatusIcon tone={c.passed ? 'done' : 'fail'} />
									<span class="font-medium">{c.check}</span>
									{#if c.flaky}<span class="text-st-needs">{t('flaky')}</span>{/if}
									{#if c.mainRed}<span class="text-st-fail">main red</span>{/if}
									{#if c.runs > 1}<span class="text-muted-foreground">{t('checkRuns', c.runs)}</span>{/if}
									{#if c.commit}<code class="text-[10px] text-muted-foreground">{c.commit.slice(0, 8)}</code>{/if}
									<span class="ml-auto text-muted-foreground">{c.runsOn} · {dur(c.seconds)} · {since(c.at)}</span>
								</li>
							{/each}
						</ul>
					</Section>
					</div>
				{/if}
			{/if}</Tabs.Content>
			<Tabs.Content value="resources" class="mt-3 space-y-4">{#if tab === 'resources'}
				<Section title={t('procTitle')} sub={t('procItemHelp')}>
					<ProcSummary p={row?.procs} />
				</Section>
				<Section title={t('procTop')} count={procRowsOf(row).length} pad={false}><ProcTable rows={procRowsOf(row)} /></Section>
				{#if row?.sessions.length}
					<Section title={t('ovSessions')} count={row.sessions.length} pad={false}>
						<ul class="divide-y">{#each row.sessions as s (s.agent + s.sessionId)}<li class="num flex flex-wrap items-center gap-x-3 gap-y-1 px-4 py-2 text-xs"><span class="font-medium">{s.agent}</span><span class="text-muted-foreground">{s.role} · {s.provider} · {s.model}{s.effort ? ' · ' + s.effort : ''}</span><span class="text-muted-foreground">{s.account} @ {s.machine}</span><StatusBadge status={s.state || 'idle'} map="agentStates" /><span class="ml-auto text-muted-foreground">{s.since ? since(s.since) : ''} · {compact(s.tokens)} · {money(s.costUsd)}</span></li>{/each}</ul>
					</Section>
				{/if}
				<Section title={t('ovWorktrees')} count={row?.worktrees.length || 0} pad={false}><WorktreeList list={row?.worktrees || []} /></Section>
			{/if}</Tabs.Content>
			{#if row?.usage}
				{@const u = row.usage}
				<Tabs.Content value="cost" class="mt-3 space-y-4">{#if tab === 'cost'}
					<div class="num grid grid-cols-2 gap-2 text-xs sm:grid-cols-4">
						{#each [[t('ovCost'), money(u.costUsd)], [t('tokens'), compact(u.tokens)], [t('ovSessions'), u.sessions], [t('ovAgentTime'), dur(u.seconds)], [t('procCpuTime'), u.cpuSeconds ? dur(u.cpuSeconds) : '—'], [t('procPeakLabel'), Number(u.peakRssBytes) ? bytes(u.peakRssBytes) : '—'], [t('reviewsN'), row.reviews], [t('deliveriesN'), row.deliveries]] as [k, v] (k)}
							<div class="rounded-lg border bg-card p-3"><p class="text-muted-foreground">{k}</p><p class="mt-1 text-lg font-semibold">{v}</p></div>
						{/each}
					</div>
					<div class="grid gap-4 lg:grid-cols-2">
						<Section title={t('costByStep')}><Bars map={cost(row.usageByStep)} fmt={money} share /></Section>
						<Section title={t('ovByModel')}><Bars map={cost(row.usageByModel)} fmt={money} share /></Section>
						<Section title={t('ovByRole')}><Bars map={cost(row.usageByRole)} fmt={money} share /></Section>
						<Section title={t('ovByMachine')}><Bars map={cost(row.usageByMachine)} fmt={money} share /></Section>
					</div>
					{#if Object.keys(row.runs).length}
						<Section title={t('runsTitle')} pad={false}>
							<ul class="divide-y">{#each Object.entries(row.runs) as [step, r] (step)}<li class="num flex flex-wrap gap-x-3 px-4 py-2 text-xs"><span class="w-28 font-medium">{step}</span><span>{r.agent}</span><span class="text-muted-foreground">{r.provider} · {r.model}{r.family ? ' · ' + r.family : ''}</span><span class="ml-auto text-muted-foreground">{r.account}</span></li>{/each}</ul>
						</Section>
					{/if}
				{/if}</Tabs.Content>
			{/if}
			<!-- The live view streams only while this tab is open (ADR-0048). -->
			<Tabs.Content value="agent" class="mt-3">{#if tab === 'agent'}<AgentLive {project} id={it.id} running={(row?.sessions || []).map((s) => s.agent + '/' + s.step).join(',')} />{/if}</Tabs.Content>
			<Tabs.Content value="documents" class="mt-3">{#if tab === 'documents'}<ItemDocuments {project} id={it.id} />{/if}</Tabs.Content>
			<Tabs.Content value="related" class="mt-3">{#if tab === 'related'}
				<Section title={t('tabRelated')} sub={t('relatedHelp')}>
					{#if !related}<p class="flex items-center gap-2 text-sm text-muted-foreground"><Spinner class="size-3.5" />{t('loading')}</p>{:else if related.error}<p class="text-sm text-destructive">{related.error}</p>{:else}<Related data={related.data || {}} {project} {id} />{/if}
				</Section>
			{/if}</Tabs.Content>
		</Tabs.Root>

		<aside class="space-y-4">
			<Section title={t('details')}>
				<KV rows={[
					[t('step'), finished(it.status) ? '' : it.step ? it.step + (it.stepStartedAt ? ' · ' + since(it.stepStartedAt) : '') : ''],
					[t('integratedAt'), it.integratedAt ? when(it.integratedAt) : ''],
					[t('leadTime'), row?.leadSeconds ? dur(row.leadSeconds) : ''],
					[t('quietFor'), !finished(it.status) && row?.quietSeconds && row.quietSeconds > 600 ? dur(row.quietSeconds) : ''],
					[t('blockedByLabel'), row?.blockedBy.join(', ')],
					[t('lastDelivery'), row?.lastDelivery?.status ? row.lastDelivery.status + ' · ' + row.lastDelivery.agent + ' · ' + since(row.lastDelivery.at) : ''],
					[t('lastActivity'), row?.lastActivity?.type ? row.lastActivity.type + ' · ' + (row.lastActivity.actor || row.lastActivity.machine) + ' · ' + since(row.lastActivity.at) : ''],
					[t('unlockedLabel'), it.unlocked.join(', ')],
					[t('holderLabel'), holder],
					[t('goal'), it.goalId],
					[t('tier'), it.tier || ''],
					[t('attempts'), it.attempts || ''],
					[t('tracker'), it.trackerRef],
					[t('deps'), it.deps.join(', ')],
					[t('updated'), since(it.updatedAt)]
				]} />
			</Section>
			{#if it.branch || it.pr || it.trackerRef || links?.issue}
				<!-- Where the item is outside Barnraise: each link opens the page. -->
				<Section title={t('itemLinks')}>
					<div class="num space-y-2 text-xs">
						{#if it.trackerRef || links?.issue}
							{#if links?.issue}<a href={links.issue} target="_blank" rel="noreferrer" class="flex items-center gap-1.5 hover:underline" title={t('openIssue')}><TicketIcon class="size-3.5 shrink-0" />{t('issueLink', it.trackerRef || links.issue)}<ExternalLinkIcon class="size-3 shrink-0" /></a>
							{:else}<p class="flex items-center gap-1.5"><TicketIcon class="size-3.5 shrink-0" />{t('issueLink', it.trackerRef)}</p>{/if}
						{/if}
						{#if row?.pr?.number}
							<a href={row.pr.url || links?.pr} target="_blank" rel="noreferrer" class="flex items-center gap-1.5 hover:underline" title={t('openPr')}><GitPullRequestIcon class="size-3.5 shrink-0" />#{row.pr.number} · {row.pr.state}<ExternalLinkIcon class="size-3 shrink-0" /></a>
						{:else if it.pr}<p class="flex items-center gap-1.5"><GitPullRequestIcon class="size-3.5 shrink-0" />{it.pr}</p>{/if}
						{#if it.branch}
							{#if links?.branch}<a href={links.branch} target="_blank" rel="noreferrer" class="flex items-center gap-1.5 break-all hover:underline" title={t('openBranch')}><GitBranchIcon class="size-3.5 shrink-0" />{it.branch}<ExternalLinkIcon class="size-3 shrink-0" /></a>
							{:else}<p class="flex items-center gap-1.5 break-all"><GitBranchIcon class="size-3.5 shrink-0" />{it.branch}</p>{/if}
						{/if}
						{#if links?.compare && !row?.pr?.number}<a href={links.compare} target="_blank" rel="noreferrer" class="flex items-center gap-1.5 hover:underline"><GitCompareIcon class="size-3.5 shrink-0" />{t('openCompare')}<ExternalLinkIcon class="size-3 shrink-0" /></a>{/if}
						{#if row?.lastReview}
							<p class="text-muted-foreground">{t('lastReview', row.lastReview.decision, row.lastReview.reviewer, row.lastReview.findings)}</p>
							{#if row.lastReview.criteriaVerdict || row.lastReview.codeVerdict}<p class="text-muted-foreground">{t('verdictCriteria', '')} <strong class="text-foreground">{verdict(row.lastReview.criteriaVerdict)}</strong> · {t('verdictCode', '')} <strong class="text-foreground">{verdict(row.lastReview.codeVerdict)}</strong></p>{/if}
						{/if}
						{#if it.mergeCommit}
							{#if links?.commit}<a href={links.commit} target="_blank" rel="noreferrer" class="flex items-center gap-1.5 text-muted-foreground hover:text-foreground hover:underline">{t('merged', it.mergeCommit)}<ExternalLinkIcon class="size-3 shrink-0" /></a>
							{:else}<p class="text-muted-foreground">{t('merged', it.mergeCommit)}</p>{/if}
						{/if}
					</div>
				</Section>
			{/if}
			{#if row?.usage && Number(row.usage.tokens)}
				{@const u = row.usage}
				<Section title={t('ovCost')}>
					<p class="num text-2xl font-semibold">{money(u.costUsd)}</p>
					<p class="num mt-1 text-xs text-muted-foreground">{t('usageShort', compact(u.tokens), u.sessions, dur(u.seconds))}</p>
					<div class="num mt-3 grid grid-cols-2 gap-2 text-[11px]">
						<div class="rounded-md bg-muted/50 p-2"><p class="text-muted-foreground">input</p><p>{compact(u.input)}</p></div>
						<div class="rounded-md bg-muted/50 p-2"><p class="text-muted-foreground">output</p><p>{compact(u.output)}</p></div>
						<div class="rounded-md bg-muted/50 p-2"><p class="text-muted-foreground">cache read</p><p>{compact(u.cacheRead)}</p></div>
						<div class="rounded-md bg-muted/50 p-2"><p class="text-muted-foreground">cache write</p><p>{compact(u.cacheWrite)}</p></div>
					</div>
				</Section>
			{/if}
			{#if est && !finished(it.status) && (est.hours || Number(est.tokens))}
				<Section title={t('estimate')}>
					<p class="text-sm">{t('estimateLine', num(est.hours), compact(est.tokens), num(est.weekPct), est.account || '—')}</p>
					{#if est.basedOnItems}<p class="mt-1 text-xs text-muted-foreground">{t('estimateBase', est.basedOnItems)}</p>{/if}
				</Section>
			{/if}
			{#if row?.procs?.count}
				<Section title={t('procTitle')}>
					{#snippet right()}<button class="text-xs text-muted-foreground hover:text-foreground hover:underline" onclick={() => (tab = 'resources')}>{t('seeAll')}</button>{/snippet}
					<ProcSummary p={row.procs} compact />
				</Section>
			{/if}
			{#if it.filesTouched.length}
				<Section title={t('filesTouched')} count={it.filesTouched.length}><ul class="num space-y-0.5 text-[11px]">{#each it.filesTouched as f (f)}<li class="break-all">{f}</li>{/each}</ul></Section>
			{/if}
		</aside>
	</div>
</Loading>
