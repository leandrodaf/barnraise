<script lang="ts">
	import { Badge } from '$lib/components/ui/badge';
	import { Button } from '$lib/components/ui/button';
	import LightbulbIcon from '@lucide/svelte/icons/lightbulb';
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

<PageHeader title={t('proposals')} icon={LightbulbIcon} meta={[session.scope || t('allProjects'), t('openN', open.length)]} subtitle={t('proposalsHelp')} />
<Loading state={r}>
	<PageGrid>
		{#if !open.length}<Section title={t('proposals')}><Empty text={t('noProposals')} /></Section>{/if}
		{#each groups as [k, list] (k)}
			<Section title={k} count={list.length} pad={false}>
				<div class="grid divide-y min-[1900px]:grid-cols-2 min-[1900px]:divide-x">
					{#each list as o (o.project + o.p.id)}
						{@const p = o.p}
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
							<div class="flex flex-wrap gap-2 sm:pl-7">
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
