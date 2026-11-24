<script lang="ts">
	import * as Tabs from '$lib/components/ui/tabs';
	import * as Collapsible from '$lib/components/ui/collapsible';
	import { Badge } from '$lib/components/ui/badge';
	import { Button } from '$lib/components/ui/button';
	import BoxesIcon from '@lucide/svelte/icons/boxes';
	import FolderIcon from '@lucide/svelte/icons/folder';
	import FileCodeIcon from '@lucide/svelte/icons/file-code';
	import ChevronRightIcon from '@lucide/svelte/icons/chevron-right';
	import SettingsIcon from '@lucide/svelte/icons/settings-2';
	import MessagesIcon from '@lucide/svelte/icons/messages-square';
	import CircleCheckIcon from '@lucide/svelte/icons/circle-check';
	import type { ItemOverview, Part, Question } from '$lib/gen/barnraise/v1/barnraise_pb';
	import { api } from '$lib/api';
	import { t } from '$lib/i18n';
	import { ago, compact, money } from '$lib/format';
	import { session } from '$lib/session.svelte';
	import { loader } from '$lib/loader.svelte';
	import { COLUMNS, COLUMN_TONE, BG, TEXT, columnLabel } from '$lib/status';
	import Loading from '$lib/app/Loading.svelte';
	import PageHeader from '$lib/app/PageHeader.svelte';
	import Section from '$lib/app/Section.svelte';
	import Empty from '$lib/app/Empty.svelte';
	import QuestionCard from '$lib/app/QuestionCard.svelte';
	import ItemRow from '$lib/app/ItemRow.svelte';
	import ApprovalButtons from '$lib/app/ApprovalButtons.svelte';
	import { discussion } from '$lib/app/discussion.svelte';

	// The parts of a project (ListParts): first what each one waits from the
	// owner, with the question itself and its answers; then how its work goes;
	// how it is set up stays folded. Which item belongs to which part, what
	// waits and what is approved are the daemon's word (area, needs_owner,
	// awaits, waiting_question).
	let picked = $state(session.scope || session.project);
	const project = $derived(session.scope || (session.projects.includes(picked) ? picked : session.projects[0]));
	const r = loader(async () => {
		const [parts, overview, questions] = await Promise.all([
			api.listParts({ project }),
			api.overview({ project, brief: true, limit: 500, finished: '7d' }),
			api.listQuestions({ project }).catch(() => null)
		]);
		return { parts, items: overview.projects.flatMap((p) => p.items), questions: questions?.questions || [] };
	});
	let first = true;
	$effect(() => {
		void project;
		if (first) return void (first = false);
		r.reload();
	});

	type Waiting = { it: ItemOverview; q?: Question };
	// What each part waits from the owner: an open question, or an approval.
	const waiting = $derived.by(() => {
		const by: Record<string, Waiting[]> = {};
		if (!r.data) return by;
		const qs = new Map(r.data.questions.filter((q) => !q.answer).map((q) => [q.id, q]));
		for (const it of r.data.items) {
			if (!it.needsOwner) continue;
			(by[it.area] ||= []).push({ it, q: it.waitingQuestion ? qs.get(it.waitingQuestion) : undefined });
		}
		return by;
	});
	const working = (p: Part) => (r.data?.items || []).filter((it) => it.area === p.name && it.column === 'working');
	const totalWaiting = $derived(Object.values(waiting).reduce((s, l) => s + l.length, 0));
	const total = (cols: Record<string, number>) => Object.values(cols).reduce((s, n) => s + n, 0);
	// Kinds that go through the same flow are said once.
	const flowsOf = (p: Part) => {
		const by = new Map<string, string[]>();
		for (const [kind, flow] of Object.entries(p.flows)) by.set(flow, [...(by.get(flow) || []), kind]);
		return [...by.entries()].sort().map(([flow, kinds]) => ({ flow, kinds: kinds.sort(), steps: p.flowSteps[flow]?.steps || [] }));
	};
</script>

<PageHeader title={t('parts')} icon={BoxesIcon} meta={[project, r.data ? t('partsN', r.data.parts.parts.length) : '', totalWaiting ? t('partsWaitingN', totalWaiting) : '']} subtitle={t('partsHelp')}>
	{#if !session.scope && session.projects.length > 1}
		<Tabs.Root bind:value={picked}><Tabs.List>{#each session.projects as p (p)}<Tabs.Trigger value={p}>{p}</Tabs.Trigger>{/each}</Tabs.List></Tabs.Root>
	{/if}
</PageHeader>

<Loading state={r}>
	{@const v = r.data!.parts}
	{#if !v.parts.length}<Section title={t('parts')}><Empty text={t('noParts')} /></Section>{/if}
	<div class="space-y-4">
		{#each v.parts as p (p.name)}
			{@const all = total(p.columns)}
			{@const wait = waiting[p.name] || []}
			{@const now = working(p)}
			<section class="min-w-0 overflow-hidden rounded-xl border bg-card {wait.length ? 'border-st-needs/40' : ''}">
				<header class="flex flex-wrap items-start gap-x-4 gap-y-2 border-b px-4 py-3">
					<div class="min-w-0 flex-1 space-y-1">
						<div class="flex flex-wrap items-center gap-2">
							<h2 class="text-base font-semibold">{p.name}</h2>
							{#if p.stack}<Badge variant="secondary" class="num">{p.stack}</Badge>{/if}
							{#if p.dir}<span class="num flex items-center gap-1 text-xs text-muted-foreground"><FolderIcon class="size-3" />{p.dir}/</span>{/if}
						</div>
						{#if p.about}<p class="text-sm text-muted-foreground">{p.about}</p>{/if}
					</div>
					<!-- The part at a glance: how many items in each column. -->
					{#if all}
						<div class="num flex flex-wrap items-center gap-1.5 text-[11px]">
							{#each COLUMNS.filter((c) => p.columns[c]) as c (c)}
								<span class="inline-flex items-center gap-1 rounded-full border px-2 py-0.5"><span class="size-1.5 rounded-full {BG[COLUMN_TONE[c]]}"></span><span class={TEXT[COLUMN_TONE[c]]}>{columnLabel(c)}</span><span class="font-semibold">{p.columns[c]}</span></span>
							{/each}
						</div>
					{/if}
				</header>

				{#if wait.length}
					<!-- What the owner has to do, said plainly, answerable here. -->
					<div class="space-y-3 border-b bg-st-needs/[0.04] p-4">
						<p class="text-sm font-medium text-st-needs">{wait.length === 1 ? t('partWaitOne') : t('partWaitMany', wait.length)}</p>
						{#each wait as w (w.it.id)}
							{#if w.q}
								<div class="space-y-1">
									<p class="num text-[11px] text-muted-foreground">{t('partWaitFor', w.it.id, w.it.title)}</p>
									<QuestionCard q={w.q} {project} />
								</div>
							{:else}
								<div class="overflow-hidden rounded-lg border bg-card">
									<ItemRow r={{ ...w.it, project }}>
										{#snippet below()}
											<p class="text-sm">{w.it.awaits === 'plan' ? t('partWaitPlan') : w.it.awaits ? t('partWaitDelivery') : w.it.detail || t('partWaitOpen')}</p>
											<div class="flex flex-wrap gap-2">
												<ApprovalButtons id={w.it.id} awaits={w.it.awaits} {project} />
												<Button size="sm" variant="secondary" onclick={() => discussion.show(project, w.it.id, w.it.title)}><MessagesIcon />{t('discuss')}</Button>
											</div>
										{/snippet}
									</ItemRow>
								</div>
							{/if}
						{/each}
					</div>
				{:else if all}
					<p class="flex items-center gap-2 border-b px-4 py-2.5 text-sm text-muted-foreground"><CircleCheckIcon class="size-4 text-st-done" />{t('partNothingForYou')}</p>
				{/if}

				{#if now.length}
					<div class="border-b">
						<p class="eyebrow px-4 pt-3 text-muted-foreground">{t('partWorkingNow')}</p>
						<div class="divide-y">{#each now as it (it.id)}<ItemRow r={{ ...it, project }} />{/each}</div>
					</div>
				{/if}

				<div class="space-y-3 px-4 py-3">
					<p class="num flex flex-wrap gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
						{#if !all}<span>{t('partNoItems')}</span>{:else}<span>{t('partOpen', p.open)}</span>{/if}
						{#if p.lastIntegratedAt}<span>{t('partLastIntegrated', ago(p.lastIntegratedAt))}</span>{/if}
						{#if p.usage && Number(p.usage.tokens)}<span>{t('partSpent', money(p.usage.costUsd), compact(p.usage.tokens), p.usage.sessions)}</span>{/if}
					</p>

					<!-- How the part is set up: read when needed, folded otherwise. -->
					<Collapsible.Root>
						<Collapsible.Trigger class="group flex items-center gap-1.5 text-xs font-medium text-muted-foreground hover:text-foreground">
							<SettingsIcon class="size-3.5" />{t('partHow')}<ChevronRightIcon class="size-3.5 transition-transform group-data-[state=open]:rotate-90" />
						</Collapsible.Trigger>
						<Collapsible.Content>
							<dl class="mt-3 grid gap-x-6 gap-y-4 text-sm sm:grid-cols-[10rem_1fr]">
								{#if Object.keys(p.flows).length}
									<dt class="text-xs text-muted-foreground">{t('partFlows')}</dt>
									<dd class="space-y-2">
										{#each flowsOf(p) as f (f.flow)}
											<div class="space-y-1">
												<p class="text-xs">{#each f.kinds as k, i (k)}{#if i}, {/if}<span class="font-medium">{k}</span>{/each}</p>
												<ol class="num flex flex-wrap items-center gap-1 text-[11px]">
													{#each f.steps as s, i (s + i)}{#if i}<li aria-hidden="true" class="text-muted-foreground">→</li>{/if}<li class="rounded bg-muted px-1.5 py-0.5">{s}</li>{/each}
												</ol>
											</div>
										{/each}
									</dd>
								{/if}
								{#if p.checks.length}
									<dt class="text-xs text-muted-foreground">{t('partChecks')}</dt>
									<dd class="flex flex-wrap gap-1">{#each p.checks as c (c)}<code class="num rounded bg-muted px-1.5 py-0.5 text-[11px]">{c}</code>{/each}</dd>
								{/if}
								{#if p.paths.length}
									<dt class="text-xs text-muted-foreground">{t('partPaths')}</dt>
									<dd class="flex flex-wrap gap-1">{#each p.paths as x (x)}<code class="num rounded bg-muted px-1.5 py-0.5 text-[11px]">{x}</code>{/each}</dd>
								{/if}
								{#if p.contracts.length}
									<dt class="text-xs text-muted-foreground">{t('partContracts')}</dt>
									<dd class="space-y-0.5">{#each p.contracts as c (c)}<p class="num text-[11px] [overflow-wrap:anywhere]">{c}</p>{/each}</dd>
								{/if}
								{#if p.standard}
									<dt class="text-xs text-muted-foreground">{t('partStandard')}</dt>
									<dd class="num text-[11px]">{p.standard}</dd>
								{/if}
							</dl>
							{#if p.config}
								<Collapsible.Root class="mt-3">
									<Collapsible.Trigger class="group flex w-full items-center gap-1.5 rounded-md border px-3 py-2 text-xs font-medium hover:bg-muted">
										<FileCodeIcon class="size-3.5" /><span class="num">{p.configFile}</span>
										<ChevronRightIcon class="ml-auto size-3.5 transition-transform group-data-[state=open]:rotate-90" />
									</Collapsible.Trigger>
									<Collapsible.Content><pre class="num mt-2 max-h-96 overflow-auto rounded-lg bg-muted/60 p-3 text-[11px] leading-relaxed">{p.config}</pre></Collapsible.Content>
								</Collapsible.Root>
							{:else if p.configFile}<p class="num mt-3 text-[11px] text-muted-foreground">{p.configFile}</p>{/if}
						</Collapsible.Content>
					</Collapsible.Root>
				</div>
			</section>
		{/each}
	</div>

	{#if v.config}
		<Collapsible.Root class="mt-4">
			<Collapsible.Trigger class="group flex w-full items-center gap-1.5 rounded-xl border bg-card px-4 py-3 text-sm font-medium hover:bg-muted">
				<FileCodeIcon class="size-4" />{t('partProjectConfig')}<span class="num text-xs font-normal text-muted-foreground">{v.configFile}</span>
				<ChevronRightIcon class="ml-auto size-4 transition-transform group-data-[state=open]:rotate-90" />
			</Collapsible.Trigger>
			<Collapsible.Content><pre class="num mt-2 max-h-[32rem] overflow-auto rounded-xl border bg-muted/60 p-3 text-[11px] leading-relaxed">{v.config}</pre></Collapsible.Content>
		</Collapsible.Root>
	{/if}
</Loading>
