<script lang="ts">
	import { Progress } from '$lib/components/ui/progress';
	import PlaneIcon from '@lucide/svelte/icons/plane';
	import HouseIcon from '@lucide/svelte/icons/house';
	import CircleHelpIcon from '@lucide/svelte/icons/circle-help';
	import ActivityIcon from '@lucide/svelte/icons/activity';
	import CheckCheckIcon from '@lucide/svelte/icons/check-check';
	import OctagonAlertIcon from '@lucide/svelte/icons/octagon-alert';
	import TriangleAlertIcon from '@lucide/svelte/icons/triangle-alert';
	import TargetIcon from '@lucide/svelte/icons/target';
	import CrosshairIcon from '@lucide/svelte/icons/crosshair';
	import { api } from '$lib/api';
	import { t } from '$lib/i18n';
	import { compact, day, dur, money, num, since } from '$lib/format';
	import { href } from '$lib/router.svelte';
	import { session } from '$lib/session.svelte';
	import { cluster } from '$lib/cluster.svelte';
	import { loader } from '$lib/loader.svelte';
	import { groups, clusterItems } from '$lib/app/items';
	import Loading from '$lib/app/Loading.svelte';
	import PageHeader from '$lib/app/PageHeader.svelte';
	import PageGrid from '$lib/app/PageGrid.svelte';
	import Section from '$lib/app/Section.svelte';
	import Kpi from '$lib/app/Kpi.svelte';
	import QuestionCard from '$lib/app/QuestionCard.svelte';
	import ApprovalButtons from '$lib/app/ApprovalButtons.svelte';
	import ItemRow from '$lib/app/ItemRow.svelte';
	import Empty from '$lib/app/Empty.svelte';

	// The questions, approvals, goals and alerts of the projects in scope.
	const st = loader((projects) => Promise.all(projects.map((project) => api.status({ project }).then((s) => ({ project, s })))));
	const g = $derived(groups(clusterItems(cluster.data)));
	const questions = $derived((st.data || []).flatMap(({ project, s }) => s.waitingOwner.map((q) => ({ project, q }))));
	// What the owner approves now: the daemon says it per item (awaits).
	const approvals = $derived(g.needs.filter((r) => r.awaits));
	const alerts = $derived((st.data || []).flatMap(({ project, s }) => s.alerts.map((a) => ({ project, a }))));
	const goals = $derived((st.data || []).filter(({ s }) => s.goal?.name));
	const focuses = $derived((st.data || []).filter(({ s }) => s.focus?.name && !s.focus.cancelled));
	// Totals of the scope, added up by the daemon.
	const usage = $derived({ tokens: cluster.data?.usage?.tokens || 0n, sessions: cluster.data?.usage?.sessions || 0, seconds: cluster.data?.usage?.seconds || 0, cost: cluster.data?.usage?.costUsd || 0 });
	const needs = $derived(questions.length + approvals.length);
</script>

<PageHeader title={t('navHome')} icon={HouseIcon} meta={[session.scope || t('allProjects'), session.machine, t('homeSpendShort', money(usage.cost), compact(usage.tokens))]} subtitle={t('homeSummary', needs, g.working.length, g.blocked.length)} />
{#if session.autopilot.length}
	<p class="mb-4 flex items-start gap-2 rounded-lg border border-st-review/40 bg-st-review/[0.06] px-3 py-2 text-sm">
		<PlaneIcon class="mt-0.5 size-4 shrink-0 text-st-review" /><span>{t('autopilotBanner', session.autopilot.join(', '))}</span>
	</p>
{/if}

<div class="mb-4 grid grid-cols-2 gap-3 lg:grid-cols-4">
	<Kpi label={t('kpiNeeds')} value={needs} tone="needs" icon={CircleHelpIcon} hint={t('kpiNeedsHint', questions.length, approvals.length)} />
	<Kpi label={t('kpiWorking')} value={g.working.length} tone="work" icon={ActivityIcon} hint={t('kpiNow')} href={href('agents')} linkText={t('kpiSeeAgents')} />
	<Kpi label={t('kpiIntegrated')} value={g.recent.length} tone="done" icon={CheckCheckIcon} hint={t('kpiLast7')} href={href('overview')} linkText={t('kpiSeeCluster')} />
	<Kpi label={t('kpiBlocked')} value={g.blocked.length} tone="fail" icon={OctagonAlertIcon} hint={t('kpiNow')} href={href('queue')} linkText={t('kpiSeeQueue')} />
</div>

<Loading state={st}>
	<PageGrid>
		<Section title={t('kpiNeeds')} count={needs} sub={t('kpiNeedsHint', questions.length, approvals.length)} pad={false}>
			{#if !needs}<div class="p-4"><Empty text={t('noWaiting')} /></div>{/if}
			{#if questions.length}
				<p class="eyebrow border-b bg-muted/30 px-4 py-2 text-muted-foreground">{t('questionsN', questions.length)}</p>
				<div class="grid gap-3 p-3 sm:p-4 min-[1900px]:grid-cols-2">{#each questions as { project, q } (project + q.id)}<QuestionCard {q} {project} />{/each}</div>
			{/if}
			{#if approvals.length}
				<p class="eyebrow border-y bg-muted/30 px-4 py-2 text-muted-foreground">{t('approvalsN', approvals.length)}</p>
				<div class="divide-y">
					{#each approvals as r (r.project + r.id)}
						<ItemRow {r}>
							{#snippet meta()}<span>{r.step}</span>{#if r.stepStartedAt}<span>{since(r.stepStartedAt)}</span>{/if}{/snippet}
							{#snippet below()}<ApprovalButtons id={r.id} awaits={r.awaits} project={r.project} />{/snippet}
						</ItemRow>
					{/each}
				</div>
			{/if}
		</Section>
		{#snippet aside()}
			<Section title={t('kpiWorking')} count={g.working.length} pad={false}>
				{#if !g.working.length}<div class="p-4"><Empty text={session.standby ? t('agentsStandby') : t('noAgents')} /></div>{:else}
					<div class="divide-y">{#each g.working.slice(0, 8) as r (r.project + r.id)}<ItemRow {r}>{#snippet meta()}<span>{r.step}</span><span>{r.machine}</span><span>{since(r.stepStartedAt)}</span>{/snippet}</ItemRow>{/each}</div>
				{/if}
			</Section>
			{#if goals.length || focuses.length}
				<Section title={t('goal')}>
					<div class="space-y-4">
						{#each goals as { project, s } (project)}
							{@const pr = s.progress}
							<div class="space-y-2">
								<div class="flex items-start gap-2"><TargetIcon class="mt-0.5 size-4 shrink-0 text-muted-foreground" /><div class="min-w-0 flex-1"><p class="text-sm font-medium">{s.goal!.name}</p><p class="num text-[11px] text-muted-foreground">{project}</p></div><span class="num text-lg font-semibold">{pr?.percentage || 0}%</span></div>
								<Progress value={pr?.percentage || 0} class="h-1.5" />
								<p class="num text-[11px] text-muted-foreground">{t('goalItems', pr?.done || 0, pr?.total || 0)} · {t('pace', num(pr?.perDay))} · {pr?.forecast ? t('forecast', day(pr.forecast)) : t('noForecast')}</p>
							</div>
						{/each}
						{#each focuses as { project, s } (project)}
							<div class="flex items-start gap-2 border-t pt-3"><CrosshairIcon class="mt-0.5 size-4 shrink-0 text-muted-foreground" /><div class="min-w-0 flex-1"><p class="text-sm font-medium">{s.focus!.name}</p><p class="num text-[11px] text-muted-foreground">{project} · {t('focusItems', s.focusDone, s.focusAll)}</p></div></div>
						{/each}
					</div>
				</Section>
			{/if}
			<Section title={t('sbRecent')} count={g.recent.length} pad={false}>
				{#if !g.recent.length}<div class="p-4"><Empty /></div>{:else}
					<div class="divide-y">{#each g.recent.slice(0, 6) as r (r.project + r.id)}<ItemRow {r}>{#snippet meta()}<span>{since(r.integratedAt)}</span>{#if r.usage}<span>{money(r.usage.costUsd)}</span>{/if}{/snippet}</ItemRow>{/each}</div>
				{/if}
			</Section>
			{#if alerts.length}
				<Section title={t('alerts')} count={alerts.length}>
					<ul class="space-y-2 text-sm">{#each alerts as { project, a }, i (i)}<li class="flex gap-2"><TriangleAlertIcon class="mt-0.5 size-4 shrink-0 text-st-needs" /><span class="min-w-0 break-words">{a} <span class="num text-[11px] text-muted-foreground">{project}</span></span></li>{/each}</ul>
				</Section>
			{/if}
			<p class="num px-1 text-[11px] text-muted-foreground">{t('homeSpend', compact(usage.tokens), usage.sessions, dur(usage.seconds), money(usage.cost))}</p>
		{/snippet}
	</PageGrid>
</Loading>
