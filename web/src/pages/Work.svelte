<script lang="ts">
	// Issues and pull requests: what this cluster works on (each item with its
	// issue, its pull request and who runs it: which machine, which agent) and
	// what other people work on in the same repository (a teammate's
	// Barnraise on its own machine, a pull request opened by hand, an issue a
	// person took), with what of ours waits for it. Nobody does the same work
	// twice when everyone can see it.
	import { onDestroy } from 'svelte';
	import { Badge } from '$lib/components/ui/badge';
	import GitPullRequestIcon from '@lucide/svelte/icons/git-pull-request';
	import TicketIcon from '@lucide/svelte/icons/ticket';
	import ServerIcon from '@lucide/svelte/icons/server';
	import UserIcon from '@lucide/svelte/icons/circle-user-round';
	import BotIcon from '@lucide/svelte/icons/bot';
	import ExternalLinkIcon from '@lucide/svelte/icons/external-link';
	import UsersIcon from '@lucide/svelte/icons/users';
	import { t } from '$lib/i18n';
	import { ago } from '$lib/format';
	import { session } from '$lib/session.svelte';
	import { cluster } from '$lib/cluster.svelte';
	import { clusterItems } from '$lib/app/items';
	import { href } from '$lib/router.svelte';
	import { otherWork, type OtherWorkView } from '$lib/rest';
	import PageHeader from '$lib/app/PageHeader.svelte';
	import Section from '$lib/app/Section.svelte';
	import Empty from '$lib/app/Empty.svelte';
	import Dot from '$lib/app/Dot.svelte';

	const rows = $derived(clusterItems(cluster.data).filter((r) => r.column !== 'done' || (r.pr && r.pr.state === 'open')));
	const tracked = $derived(rows.filter((r) => r.trackerRef || r.pr || r.links?.issue));

	let others = $state<Record<string, OtherWorkView>>({});
	let timer: ReturnType<typeof setTimeout> | undefined;
	let alive = true;
	async function load() {
		const next: Record<string, OtherWorkView> = {};
		for (const p of session.scoped) {
			try {
				next[p] = await otherWork(p);
			} catch {
				/* a project without a repository */
			}
		}
		if (!alive) return;
		others = next;
		timer = setTimeout(load, 30000);
	}
	$effect(() => {
		void session.scoped.join(',');
		clearTimeout(timer);
		load();
	});
	onDestroy(() => {
		alive = false;
		clearTimeout(timer);
	});

	const work = $derived(Object.entries(others).flatMap(([project, v]) => v.work.map((w) => ({ ...w, project }))));
	const assigned = $derived(Object.entries(others).flatMap(([project, v]) => v.assigned.map((a) => ({ ...a, project }))));
	const who = (r: (typeof rows)[number]) => {
		const agents = (r.sessions || []).map((s) => s.agent).filter(Boolean);
		return agents.length ? agents.join(', ') : '';
	};
</script>

<PageHeader title={t('navWork')} icon={GitPullRequestIcon} meta={[session.scope || t('allProjects'), t('workMine', tracked.length), t('workOthers', work.length)]} subtitle={t('workPageHelp')} />

<div class="space-y-4">
	<Section title={t('workThisCluster')} count={tracked.length} pad={false}>
		{#if !tracked.length}<Empty text={t('workNoneMine')} />{:else}
			<ul class="divide-y">
				{#each tracked as r (r.project + r.id)}
					<li class="flex flex-wrap items-center gap-x-4 gap-y-1.5 px-4 py-3 text-sm">
						<a href={href('item', r.project, r.id)} class="min-w-0 flex-1 basis-72 hover:underline">
							<span class="num text-muted-foreground">{r.id}</span> <span class="font-medium">{r.title}</span>
							<span class="block text-xs text-muted-foreground">{r.project} · {r.status}{r.step ? ' · ' + r.step : ''}</span>
						</a>
						<span class="flex flex-wrap items-center gap-3 text-xs">
							{#if r.links?.issue}<a href={r.links.issue} target="_blank" rel="noreferrer" class="inline-flex items-center gap-1 hover:underline"><TicketIcon class="size-3.5" />{r.trackerRef || t('workIssue')}<ExternalLinkIcon class="size-3" /></a>
							{:else if r.trackerRef}<span class="inline-flex items-center gap-1"><TicketIcon class="size-3.5" />{r.trackerRef}</span>{/if}
							{#if r.pr?.number}<a href={r.pr.url} target="_blank" rel="noreferrer" class="inline-flex items-center gap-1 hover:underline"><GitPullRequestIcon class="size-3.5" />#{r.pr.number} <span class="text-muted-foreground">{r.pr.state}</span><ExternalLinkIcon class="size-3" /></a>{/if}
						</span>
						<span class="flex min-w-48 items-center gap-1.5 text-xs text-muted-foreground">
							{#if r.machine}
								<ServerIcon class="size-3.5 shrink-0" /><span class="font-medium text-foreground">{r.machine}</span>
								{#if who(r)}<BotIcon class="ml-1 size-3.5 shrink-0" /><span class="truncate">{who(r)}</span>{/if}
							{:else}{t('workNobody')}{/if}
						</span>
					</li>
				{/each}
			</ul>
		{/if}
	</Section>

	<Section title={t('workOtherPeople')} count={work.length} sub={t('workOtherHelp')} pad={false}>
		{#if !work.length}<Empty text={t('workNoneOthers')} />{:else}
			<ul class="divide-y">
				{#each work as w (w.project + w.id)}
					<li class="flex flex-wrap items-center gap-x-4 gap-y-1.5 px-4 py-3 text-sm">
						<span class="min-w-0 flex-1 basis-72">
							{#if w.url}<a href={w.url} target="_blank" rel="noreferrer" class="hover:underline"><span class="num text-muted-foreground">{w.id}</span> <span class="font-medium">{w.title || w.branch}</span></a>
							{:else}<span class="num text-muted-foreground">{w.id}</span> <span class="font-medium">{w.title || w.branch}</span>{/if}
							<span class="block text-xs text-muted-foreground">{w.project} · <code class="num">{w.branch}</code> · {t('workPrFiles', w.files)}</span>
						</span>
						<span class="flex items-center gap-1.5 text-xs">
							{#if w.kind === 'cluster'}
								<Badge variant="outline">{t('workKindCluster')}</Badge>
								<ServerIcon class="size-3.5 text-muted-foreground" /><span class="font-medium">{w.machine || t('workUnknownMachine')}</span>
							{:else}
								<Badge variant="outline">{t('workKindPR')}</Badge>
								<UserIcon class="size-3.5 text-muted-foreground" /><span class="font-medium">{w.author}</span>
							{/if}
						</span>
						<span class="flex items-center gap-1.5 text-xs text-muted-foreground">
							<Dot tone={w.stale ? 'idle' : 'work'} />{w.stale ? t('workStale') : t('workActive')}{#if w.updated} · {ago(w.updated)}{/if}
						</span>
						{#if w.holds?.length}
							<span class="basis-full text-xs text-st-needs">{t('workHolds')} {#each w.holds as id, i (id)}{i ? ', ' : ''}<a class="num hover:underline" href={href('item', w.project, id)}>{id}</a>{/each}</span>
						{/if}
					</li>
				{/each}
			</ul>
		{/if}
	</Section>

	{#if assigned.length}
		<Section title={t('workAssigned')} count={assigned.length} sub={t('workAssignedHelp')} pad={false}>
			<ul class="divide-y">
				{#each assigned as a (a.project + a.id)}
					<li class="flex flex-wrap items-center gap-x-4 gap-y-1 px-4 py-3 text-sm">
						<a href={href('item', a.project, a.id)} class="min-w-0 flex-1 hover:underline"><span class="num text-muted-foreground">{a.id}</span> {a.title}</a>
						{#if a.url}<a href={a.url} target="_blank" rel="noreferrer" class="inline-flex items-center gap-1 text-xs hover:underline"><TicketIcon class="size-3.5" />{a.ref}</a>{/if}
						<span class="inline-flex items-center gap-1 text-xs"><UsersIcon class="size-3.5 text-muted-foreground" />{a.people.join(', ')}</span>
					</li>
				{/each}
			</ul>
		</Section>
	{/if}
</div>
