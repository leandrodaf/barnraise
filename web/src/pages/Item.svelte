<script lang="ts">
	import * as Tabs from '$lib/components/ui/tabs';
	import { Button } from '$lib/components/ui/button';
	import { Badge } from '$lib/components/ui/badge';
	import CheckIcon from '@lucide/svelte/icons/circle-check';
	import CircleIcon from '@lucide/svelte/icons/circle-dashed';
	import GitBranchIcon from '@lucide/svelte/icons/git-branch';
	import GitPullRequestIcon from '@lucide/svelte/icons/git-pull-request';
	import ExternalLinkIcon from '@lucide/svelte/icons/external-link';
	import HandIcon from '@lucide/svelte/icons/hand';
	import UndoIcon from '@lucide/svelte/icons/undo-2';
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
	import QuestionCard from '$lib/app/QuestionCard.svelte';

	let { id, project }: { id: string; project: string } = $props();
	const data = loader(async () => {
		const [ir, tl, est, qs, ov] = await Promise.all([
			api.getItem({ project, id }),
			api.timeline({ project, id }).catch(() => null),
			api.estimate({ project, id }).catch(() => null),
			api.listQuestions({ project }).catch(() => null),
			api.overview({ project, item: id, all: true }).catch(() => null)
		]);
		// The open questions about this item, answered right here.
		const questions = (qs?.questions || []).filter((q) => q.itemId === id && !q.answer);
		// The board's facts of this item (flow, what it awaits, signal, checks), from the daemon.
		const row = ov?.projects[0]?.items.find((r) => r.id === id);
		return { it: ir.item!, entries: tl?.entries || [], est, questions, row: row ? ({ ...row, project } as Row) : undefined };
	});
	let tab = $state('summary');

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
					{#if it.size}<Badge variant="outline">{it.size}</Badge>{/if}
					{#if it.risky}<Badge variant="destructive">{t('risky')}</Badge>{/if}
					{#if it.parked}<Badge variant="secondary">{t('parked')}</Badge>{/if}
					{#if sub}<span class="num ml-1 text-xs {TEXT[sub.tone]}">{sub.text}</span>{/if}
				</div>
			</div>
			<div class="flex flex-wrap gap-2">
				{#if row?.awaits}<ApprovalButtons id={it.id} awaits={row.awaits} {project} />{/if}
				{#if it.status === 'paused'}<Button size="sm" variant="outline" onclick={handback}><UndoIcon />{t('handback')}</Button>
				{:else if it.status !== 'integrated' && it.status !== 'closed'}<Button size="sm" variant="outline" onclick={takeover}><HandIcon />{t('takeover')}</Button>{/if}
			</div>
		</div>
		<Stepper flow={row?.flow || []} {tone} />
		{#each questions as q (q.id)}<QuestionCard {q} {project} />{:else}{#if it.waitingQuestion}<div class="rounded-lg border border-l-2 border-l-st-needs bg-st-needs/5 p-3 text-sm">{t('waitingQuestion', it.waitingQuestion)}</div>{/if}{/each}
	</header>

	<div class="grid gap-4 xl:grid-cols-[minmax(0,1fr)_22rem]">
		<Tabs.Root bind:value={tab} class="min-w-0">
			<Tabs.List>
				<Tabs.Trigger value="summary">{t('tabSummary')}</Tabs.Trigger>
				<Tabs.Trigger value="timeline">{t('timeline')} <span class="num ml-1 text-[11px] text-muted-foreground">{entries.length}</span></Tabs.Trigger>
				<Tabs.Trigger value="criteria">{t('criteria')} <span class="num ml-1 text-[11px] text-muted-foreground">{it.criteria.filter((c) => c.met).length}/{it.criteria.length}</span></Tabs.Trigger>
				{#if row?.checks.length}<Tabs.Trigger value="checks">{t('tabChecks')} <span class="num ml-1 text-[11px] text-muted-foreground">{row.checks.length}</span></Tabs.Trigger>{/if}
			</Tabs.List>
			<Tabs.Content value="summary" class="mt-3 space-y-4">
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
			<Tabs.Content value="timeline" class="mt-3"><Section title={t('timeline')} count={entries.length}><Timeline item={it} {entries} {project} /></Section></Tabs.Content>
			<Tabs.Content value="criteria" class="mt-3">
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
			</Tabs.Content>
			{#if row?.checks.length}
				<Tabs.Content value="checks" class="mt-3">
					<Section title={t('tabChecks')} pad={false}>
						<ul class="divide-y">
							{#each row.checks as c (c.check + c.at)}
								<li class="num flex items-center gap-3 px-4 py-2.5 text-xs">
									<StatusIcon tone={c.passed ? 'done' : 'fail'} />
									<span class="font-medium">{c.check}</span>
									{#if c.flaky}<span class="text-st-needs">{t('flaky')}</span>{/if}
									{#if c.mainRed}<span class="text-st-fail">main red</span>{/if}
									<span class="ml-auto text-muted-foreground">{c.runsOn} · {dur(c.seconds)} · {since(c.at)}</span>
								</li>
							{/each}
						</ul>
					</Section>
				</Tabs.Content>
			{/if}
		</Tabs.Root>

		<aside class="space-y-4">
			<Section title={t('details')}>
				<KV rows={[
					[t('step'), it.step ? it.step + (it.stepStartedAt ? ' · ' + since(it.stepStartedAt) : '') : ''],
					[t('holderLabel'), holder],
					[t('goal'), it.goalId],
					[t('tier'), it.tier || ''],
					[t('attempts'), it.attempts || ''],
					[t('tracker'), it.trackerRef],
					[t('deps'), it.deps.join(', ')],
					[t('updated'), since(it.updatedAt)]
				]} />
			</Section>
			{#if it.branch || it.pr}
				<Section title={t('pr')}>
					<div class="num space-y-2 text-xs">
						{#if it.branch}<p class="flex items-center gap-1.5 break-all"><GitBranchIcon class="size-3.5 shrink-0" />{it.branch}</p>{/if}
						{#if row?.pr?.number}
							<a href={row.pr.url} target="_blank" rel="noreferrer" class="flex items-center gap-1.5 hover:underline"><GitPullRequestIcon class="size-3.5" />#{row.pr.number} · {row.pr.state}<ExternalLinkIcon class="size-3" /></a>
						{:else if it.pr}<p class="flex items-center gap-1.5"><GitPullRequestIcon class="size-3.5" />{it.pr}</p>{/if}
						{#if row?.lastReview}<p class="text-muted-foreground">{t('lastReview', row.lastReview.decision, row.lastReview.reviewer, row.lastReview.findings)}</p>{/if}
						{#if it.mergeCommit}<p class="text-muted-foreground">{t('merged', it.mergeCommit)}</p>{/if}
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
			{#if est && (est.hours || Number(est.tokens))}
				<Section title={t('estimate')}>
					<p class="text-sm">{t('estimateLine', num(est.hours), compact(est.tokens), num(est.weekPct), est.account || '—')}</p>
					{#if est.basedOnItems}<p class="mt-1 text-xs text-muted-foreground">{t('estimateBase', est.basedOnItems)}</p>{/if}
				</Section>
			{/if}
			{#if row?.worktrees.length}
				<Section title={t('ovWorktrees')} count={row.worktrees.length} pad={false}>
					<ul class="divide-y">{#each row.worktrees as w (w.machine + w.path)}<li class="num px-4 py-2 text-[11px]"><p class="flex justify-between"><span class="font-medium">{w.machine}</span><span class="text-muted-foreground">{bytes(w.sizeBytes)}</span></p><p class="truncate text-muted-foreground" title={w.path}>{w.path}</p></li>{/each}</ul>
				</Section>
			{/if}
			{#if it.filesTouched.length}
				<Section title={t('filesTouched')} count={it.filesTouched.length}><ul class="num space-y-0.5 text-[11px]">{#each it.filesTouched as f (f)}<li class="break-all">{f}</li>{/each}</ul></Section>
			{/if}
		</aside>
	</div>
</Loading>
