<script lang="ts">
	// The proof of an item's work: its pull request, its branch and the
	// commits pushed for it, each delivery's commit marked with its step.
	import type { ItemWork } from '$lib/rest';
	import { t } from '$lib/i18n';
	import { since, when } from '$lib/format';
	import Section from './Section.svelte';
	import GitPullRequestIcon from '@lucide/svelte/icons/git-pull-request';
	import GitBranchIcon from '@lucide/svelte/icons/git-branch';
	import GitCommitIcon from '@lucide/svelte/icons/git-commit-horizontal';
	import GitMergeIcon from '@lucide/svelte/icons/git-merge';
	import GitCompareIcon from '@lucide/svelte/icons/git-compare';
	import ExternalLinkIcon from '@lucide/svelte/icons/external-link';
	import ShieldCheckIcon from '@lucide/svelte/icons/shield-check';

	let { work }: { work: ItemWork } = $props();

	const prTone: Record<string, string> = {
		draft: 'border-muted-foreground/30 bg-muted text-muted-foreground',
		ready: 'border-st-work/30 bg-st-work/10 text-st-work',
		merged: 'border-st-done/30 bg-st-done/10 text-st-done',
		closed: 'border-st-fail/30 bg-st-fail/10 text-st-fail'
	};
	const prLabel: Record<string, string> = { draft: 'workPrDraft', ready: 'workPrReady', merged: 'workPrMerged', closed: 'workPrClosed' };
	const short = (sha: string) => sha.slice(0, 7);
	const who = (author?: string) => (author || '').replace(/\s*<[^>]*>$/, '');
	const delivered = $derived(work.commits.filter((c) => c.step).length);
</script>

<Section title={t('workTitle')} sub={t('workHelp')} count={work.commits.length || undefined} pad={false}>
	<div class="flex flex-wrap items-center gap-2 border-b px-4 py-3 text-xs">
		{#if work.pr?.number}
			{@const state = work.pr.state || 'draft'}
			<a href={work.pr.url} target="_blank" rel="noreferrer" class="inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 font-medium transition-colors hover:brightness-110 {prTone[state] || prTone.draft}">
				<GitPullRequestIcon class="size-3.5" />{t('workPr', work.pr.number)}<span class="opacity-70">· {t(prLabel[state] || 'workPrDraft')}</span><ExternalLinkIcon class="size-3 opacity-60" />
			</a>
		{/if}
		{#if work.merged}
			<a href={work.merged_url} target="_blank" rel="noreferrer" class="inline-flex items-center gap-1.5 rounded-full border border-st-done/30 bg-st-done/10 px-2.5 py-1 font-medium text-st-done hover:brightness-110">
				<GitMergeIcon class="size-3.5" />{t('workMerged', short(work.merged))}<ExternalLinkIcon class="size-3 opacity-60" />
			</a>
		{:else if work.branch}
			{#if work.branch_url}
				<a href={work.branch_url} target="_blank" rel="noreferrer" class="num inline-flex min-w-0 max-w-[15rem] items-center gap-1.5 rounded-full border px-2.5 py-1 text-muted-foreground hover:text-foreground" title={work.branch}><GitBranchIcon class="size-3.5 shrink-0" /><span class="truncate">{work.branch}</span></a>
			{:else}
				<span class="num inline-flex min-w-0 max-w-[15rem] items-center gap-1.5 rounded-full border px-2.5 py-1 text-muted-foreground" title={work.branch}><GitBranchIcon class="size-3.5 shrink-0" /><span class="truncate">{work.branch}</span></span>
			{/if}
			{#if work.compare}<a href={work.compare} target="_blank" rel="noreferrer" class="inline-flex items-center gap-1.5 rounded-full px-2 py-1 text-muted-foreground hover:text-foreground hover:underline"><GitCompareIcon class="size-3.5" />{t('workCompare')}</a>{/if}
		{/if}
		{#if delivered}<span class="ml-auto inline-flex shrink-0 items-center gap-1 text-st-done" title={t('workDeliveredHelp')}><ShieldCheckIcon class="size-3.5" />{t('workDelivered', delivered, work.steps.filter((s) => s.commit).length)}</span>{/if}
	</div>
	{#if work.commits.length}
		<ol class="relative px-4 py-2">
			{#each work.commits as c (c.sha)}
				<li class="relative flex items-start gap-3 py-2 pl-6 text-sm">
					<span class="absolute top-0 bottom-0 left-[7px] w-px bg-border" aria-hidden="true"></span>
					<span class="absolute top-2.5 left-0 grid size-[15px] place-items-center rounded-full border bg-card {c.step ? 'border-st-done/50 text-st-done' : 'text-muted-foreground'}"><GitCommitIcon class="size-2.5" /></span>
					<div class="min-w-0 flex-1">
						<p class="flex min-w-0 items-baseline gap-2">
							{#if c.url}<a href={c.url} target="_blank" rel="noreferrer" title={t('workOpenCommit')} class="num shrink-0 rounded bg-muted px-1.5 py-0.5 text-[11px] font-medium hover:bg-foreground hover:text-background">{short(c.sha)}</a>{:else}<code class="num shrink-0 rounded bg-muted px-1.5 py-0.5 text-[11px]">{short(c.sha)}</code>{/if}
							<span class="truncate">{c.subject}</span>
						</p>
						<p class="num mt-1 flex flex-wrap items-center gap-x-2 text-[11px] text-muted-foreground">
							{#if c.step}<span class="rounded-full bg-st-done/10 px-1.5 text-st-done">{t('workStepCommit', c.step)}</span>{/if}
							{#if who(c.author)}<span>{who(c.author)}</span>{/if}
							{#if c.at}<span title={when(c.at)}>{since(c.at)}</span>{/if}
							<span>{c.files === 1 ? t('workFile') : t('workFiles', c.files)}</span>
						</p>
					</div>
				</li>
			{/each}
		</ol>
		{#if work.more}<p class="border-t px-4 py-2 text-xs text-muted-foreground">{t('workMore')}</p>{/if}
	{:else}
		<p class="px-4 py-3 text-sm text-muted-foreground">{work.warning || t('workNone')}</p>
	{/if}
	{#if work.warning && work.commits.length}<p class="border-t px-4 py-2 text-xs text-st-needs">{work.warning}</p>{/if}
</Section>
