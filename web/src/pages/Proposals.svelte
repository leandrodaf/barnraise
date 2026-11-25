<script lang="ts">
	import { Badge } from '$lib/components/ui/badge';
	import { Button } from '$lib/components/ui/button';
	import LightbulbIcon from '@lucide/svelte/icons/lightbulb';
	import MessagesIcon from '@lucide/svelte/icons/messages-square';
	import SparklesIcon from '@lucide/svelte/icons/sparkles';
	import WandIcon from '@lucide/svelte/icons/wand-sparkles';
	import { toast } from 'svelte-sonner';
	import { Spinner } from '$lib/components/ui/spinner';
	import { errorText } from '$lib/api';
	import { dur } from '$lib/format';
	import { discussion, triage, triageKey } from '$lib/app/discussion.svelte';
	import type { Proposal } from '$lib/gen/barnraise/v1/barnraise_pb';
	import { api } from '$lib/api';
	import { t } from '$lib/i18n';
	import { ago, day } from '$lib/format';
	import { session } from '$lib/session.svelte';
	import { loader } from '$lib/loader.svelte';
	import { act } from '$lib/app/act';
	import { dialog } from '$lib/app/dialog.svelte';
	import Loading from '$lib/app/Loading.svelte';
	import PageHeader from '$lib/app/PageHeader.svelte';
	import PageGrid from '$lib/app/PageGrid.svelte';
	import Section from '$lib/app/Section.svelte';
	import Bars from '$lib/app/Bars.svelte';
	import Empty from '$lib/app/Empty.svelte';
	import StatusIcon from '$lib/app/StatusIcon.svelte';
	import KV from '$lib/app/KV.svelte';

	type Open = { project: string; p: Proposal };
	// Open proposals of the projects in scope, grouped by project and area.
	const r = loader((projects) => Promise.all(projects.map((project) => api.listProposals({ project }).then((x) => ({ project, x })))));
	const open = $derived(
		(r.data || []).flatMap(({ project, x }) => {
			const all = x.proposals.length ? x.proposals : x.groups.flatMap((g) => g.proposals);
			return all.filter((p) => !p.status || p.status === 'open' || p.status === 'later').map((p) => ({ project, p }));
		})
	);
	const groups = $derived.by(() => {
		const by = new Map<string, Open[]>();
		for (const o of open) {
			const k = (session.scope ? '' : o.project + ' · ') + (o.p.area || t('noArea'));
			by.set(k, [...(by.get(k) || []), o]);
		}
		return [...by.entries()];
	});
	const count = (key: (p: Proposal) => string) => open.reduce((m, o) => ((m[key(o.p) || '—'] = (m[key(o.p) || '—'] || 0) + 1), m), {} as Record<string, number>);
	const decide = (o: Open, decision: string, reason = '') => api.decideProposal({ project: o.project, id: o.p.id, decision, reason });
	const decisionLabel = (d: string) => t(d === 'approve' ? 'approveProposal' : d === 'reject' ? 'reject' : 'later');
	const tone = (d: string) => (d === 'approve' ? 'bg-st-done/15 text-st-done' : d === 'reject' ? 'bg-st-fail/15 text-st-fail' : 'bg-muted text-muted-foreground');

	// Triage: the advisor's word on every open proposal, one answer for each
	// project. Nothing is applied until the owner confirms.
	let now = $state(Date.now());
	let applying = $state(false);
	$effect(() => {
		if (!triage.busy) return;
		const id = setInterval(() => (now = Date.now()), 1000);
		return () => clearInterval(id);
	});
	// A verdict counts while its proposal is still open and it changes something.
	const verdictOf = (o: Open) => {
		const v = triage.verdicts[triageKey(o.project, o.p.id)];
		return v && !(v.decision === 'later' && o.p.status === 'later') ? v : undefined;
	};
	const pending = $derived(open.filter((o) => verdictOf(o)));
	const chosen = $derived(pending.filter((o) => !verdictOf(o)!.skip));
	const tally = (d: string) => pending.filter((o) => verdictOf(o)!.decision === d).length;
	async function runTriage() {
		if (triage.busy) return;
		const projects = [...new Set(open.map((o) => o.project))];
		triage.busy = true;
		triage.started = Date.now();
		now = triage.started;
		const next: typeof triage.verdicts = {};
		const failed: string[] = [];
		await Promise.all(
			projects.map(async (project) => {
				try {
					const r = await api.triageProposals({ project });
					for (const v of r.verdicts) next[triageKey(project, v.id)] = { project, id: v.id, decision: v.decision, reason: v.reason, skip: false };
				} catch (e) {
					if (!session.failed(e)) failed.push(project + ': ' + errorText(e));
				}
			})
		);
		triage.verdicts = next;
		triage.busy = false;
		if (failed.length) toast.error(t('error', failed.join(' · ')));
		else if (!Object.keys(next).length) toast.message(t('triageEmpty'));
	}
	async function applyTriage() {
		applying = true;
		let ok = 0;
		const failed: string[] = [];
		for (const o of chosen) {
			const v = verdictOf(o)!;
			try {
				await decide(o, v.decision, t('triageReason', v.reason));
				ok++;
				delete triage.verdicts[triageKey(o.project, o.p.id)];
			} catch (e) {
				if (session.failed(e)) break;
				failed.push(o.p.id + ' (' + errorText(e) + ')');
			}
		}
		applying = false;
		if (failed.length) toast.error(t('triageFailed', ok, failed.length, failed.join(', ')));
		else toast.success(t('triageApplied', ok));
		session.bump(0);
	}
	async function follow(o: Open) {
		const v = verdictOf(o)!;
		const r = await act(() => decide(o, v.decision, t('triageReason', v.reason)), t('decided', o.p.id, decisionLabel(v.decision)));
		if (r) delete triage.verdicts[triageKey(o.project, o.p.id)];
	}

	function reject(o: Open) {
		dialog.open({
			title: t('rejectTitle', o.p.id),
			fields: [{ name: 'reason', label: t('reason'), type: 'textarea' }],
			submit: t('reject'),
			onSubmit: async (v) => {
				await decide(o, 'reject', v.reason);
				return t('decided', o.p.id, t('reject'));
			}
		});
	}
</script>

<PageHeader title={t('proposals')} icon={LightbulbIcon} meta={[session.scope || t('allProjects'), t('openN', open.length)]} subtitle={t('proposalsHelp')}>
	{#snippet actions()}
		{#if open.length}
			<Button size="sm" variant="secondary" onclick={runTriage} disabled={triage.busy || applying || open.every((o) => session.advisorDown[o.project])} title={open.every((o) => session.advisorDown[o.project]) ? t('advisorDown', Object.values(session.advisorDown)[0] || '') : t('triageHelp')}>
				{#if triage.busy}<Spinner />{:else}<SparklesIcon />{/if}{t('triageRun')}
			</Button>
		{/if}
	{/snippet}
</PageHeader>
<Loading state={r}>
	<PageGrid>
		{#each Object.keys(session.autopilotWorking).filter((k) => k.endsWith('/proposals')) as k (k)}
			<div class="flex items-center gap-2 rounded-xl border border-st-review/40 bg-st-review/[0.06] p-3 text-sm text-st-review" role="status">
				<Spinner class="size-4" />{t('autopilotTriaging', k.split('/')[0])}
			</div>
		{/each}
		{#if triage.busy}
			<div class="flex items-center gap-2 rounded-xl border border-dashed p-3 text-sm" role="status">
				<Spinner class="size-4 text-st-review" />{t('triageRunning')}<span class="num ml-auto text-xs text-muted-foreground">{dur((now - triage.started) / 1000)}</span>
			</div>
		{:else if pending.length}
			<section class="space-y-3 rounded-xl border border-st-review/40 bg-st-review/[0.06] p-4" aria-label={t('triageRun')}>
				<p class="eyebrow flex items-center gap-1.5 text-st-review"><SparklesIcon class="size-3.5" />{t('triageRun')}</p>
				<p class="text-sm">{t('triageSummary', tally('approve'), tally('reject'), tally('later'))} <span class="text-muted-foreground">{t('triageNothing')}</span></p>
				<div class="flex flex-wrap gap-2">
					<Button size="sm" onclick={applyTriage} disabled={applying || !chosen.length}>{#if applying}<Spinner />{:else}<WandIcon />{/if}{t('triageApply', chosen.length)}</Button>
					<Button size="sm" variant="ghost" onclick={() => triage.clear()} disabled={applying}>{t('triageDiscard')}</Button>
				</div>
			</section>
		{/if}
		{#if !open.length}<Section title={t('proposals')}><Empty text={t('noProposals')} /></Section>{/if}
		{#each groups as [k, list] (k)}
			<Section title={k} count={list.length} pad={false}>
				<div class="grid divide-y min-[1900px]:grid-cols-2 min-[1900px]:divide-x">
					{#each list as o (o.project + o.p.id)}
						{@const p = o.p}
						{@const v = verdictOf(o)}
						<article class="space-y-3 px-4 py-4">
							<div class="flex items-start gap-3">
								<StatusIcon tone={p.status === 'later' ? 'idle' : 'review'} class="mt-0.5" />
								<div class="min-w-0 flex-1 space-y-1.5">
									<p class="font-medium leading-snug">{p.title}</p>
									<p class="num flex flex-wrap gap-x-3 text-[11px] text-muted-foreground">
										<span>{p.id}</span>{#if p.kind}<span>{p.kind}</span>{/if}<span>P{p.priority}</span>{#if p.size}<span>{p.size}</span>{/if}
										{#if p.by}<span>{t('by', p.by)} · {ago(p.createdAt)}</span>{/if}
										{#if p.status === 'later' && p.laterUntil}<span class="text-st-needs">{t('later')} · {day(p.laterUntil)}</span>{/if}
									</p>
								</div>
							</div>
							<div class="sm:pl-7"><KV rows={[[t('problem'), p.problem], [t('evidenceLabel'), p.evidence], [t('impact'), p.impact], [t('goalLink'), p.goalLink]]} /></div>
							{#if p.comments.length}<ul class="list-disc pl-12 text-sm text-muted-foreground">{#each p.comments as c, i (i)}<li>{c}</li>{/each}</ul>{/if}
							{#if v}
								<div class="flex items-start gap-2.5 rounded-lg border border-st-review/30 bg-st-review/[0.05] p-3 sm:ml-7">
									<input type="checkbox" class="mt-1 size-4 shrink-0 accent-current" checked={!v.skip} onchange={(e) => (v.skip = !e.currentTarget.checked)} aria-label={t('triageInclude')} title={t('triageInclude')} disabled={applying} />
									<div class="min-w-0 flex-1 space-y-1.5">
										<p class="flex flex-wrap items-center gap-2 text-sm"><span class="rounded-full px-2 py-0.5 text-xs font-medium {tone(v.decision)}">{t('triageAdvisor', decisionLabel(v.decision))}</span></p>
										{#if v.reason}<p class="text-sm text-muted-foreground [overflow-wrap:anywhere]">{v.reason}</p>{/if}
									</div>
									<Button size="sm" variant="outline" class="shrink-0" onclick={() => follow(o)} disabled={applying}>{t('triageFollow')}</Button>
								</div>
							{/if}
							<div class="flex flex-wrap gap-2 sm:pl-7">
								<Button size="sm" variant="secondary" disabled={!!session.advisorDown[o.project]} title={session.advisorDown[o.project] ? t('advisorDown', session.advisorDown[o.project]) : ''} onclick={() => discussion.show(o.project, p.id, p.title)}><MessagesIcon />{t('discuss')}</Button>
								<Button size="sm" variant="secondary" disabled={!!session.advisorDown[o.project]} title={session.advisorDown[o.project] ? t('advisorDown', session.advisorDown[o.project]) : t('decideForMeHelp')} onclick={() => discussion.show(o.project, p.id, p.title, true)}><WandIcon />{t('decideForMe')}</Button>
								<Button size="sm" onclick={() => act(() => decide(o, 'approve'), t('decided', p.id, t('approveProposal')))}>{t('approveProposal')}</Button>
								<Button size="sm" variant="outline" onclick={() => reject(o)}>{t('reject')}</Button>
								<Button size="sm" variant="ghost" onclick={() => act(() => decide(o, 'later'), t('decided', p.id, t('later')))}>{t('later')}</Button>
							</div>
						</article>
					{/each}
				</div>
			</Section>
		{/each}
		{#snippet aside()}
			<Section title={t('byArea')}><Bars map={count((p) => p.area)} fmt={String} /></Section>
			<Section title={t('byKind')}><Bars map={count((p) => p.kind)} fmt={String} /></Section>
			<Section title={t('byAuthor')}><Bars map={count((p) => p.by)} fmt={String} /></Section>
		{/snippet}
	</PageGrid>
</Loading>
