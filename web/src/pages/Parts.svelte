<script lang="ts">
	import * as Tabs from '$lib/components/ui/tabs';
	import * as Collapsible from '$lib/components/ui/collapsible';
	import { Badge } from '$lib/components/ui/badge';
	import BoxesIcon from '@lucide/svelte/icons/boxes';
	import FolderIcon from '@lucide/svelte/icons/folder';
	import FileCodeIcon from '@lucide/svelte/icons/file-code';
	import ChevronRightIcon from '@lucide/svelte/icons/chevron-right';
	import ArrowRightIcon from '@lucide/svelte/icons/arrow-right';
	import { api } from '$lib/api';
	import { t } from '$lib/i18n';
	import { ago, compact, money } from '$lib/format';
	import { href } from '$lib/router.svelte';
	import { session } from '$lib/session.svelte';
	import { loader } from '$lib/loader.svelte';
	import { COLUMNS, COLUMN_TONE, BG, TEXT, columnLabel } from '$lib/status';
	import Loading from '$lib/app/Loading.svelte';
	import PageHeader from '$lib/app/PageHeader.svelte';
	import Section from '$lib/app/Section.svelte';
	import Empty from '$lib/app/Empty.svelte';

	// The parts of a project, as the daemon describes them (ListParts): each
	// with its own barnraise.yaml, its flows, its checks and its work.
	let picked = $state(session.scope || session.project);
	const project = $derived(session.scope || (session.projects.includes(picked) ? picked : session.projects[0]));
	const r = loader(() => api.listParts({ project }));
	let first = true;
	$effect(() => {
		void project;
		if (first) return void (first = false);
		r.reload();
	});
	const total = (cols: Record<string, number>) => Object.values(cols).reduce((s, n) => s + n, 0);
</script>

<PageHeader title={t('parts')} icon={BoxesIcon} meta={[project, r.data ? t('partsN', r.data.parts.length) : '']} subtitle={t('partsHelp')}>
	{#if !session.scope && session.projects.length > 1}
		<Tabs.Root bind:value={picked}><Tabs.List>{#each session.projects as p (p)}<Tabs.Trigger value={p}>{p}</Tabs.Trigger>{/each}</Tabs.List></Tabs.Root>
	{/if}
</PageHeader>

<Loading state={r}>
	{@const v = r.data!}
	{#if !v.parts.length}<Section title={t('parts')}><Empty text={t('noParts')} /></Section>{/if}
	<div class="grid gap-4 2xl:grid-cols-2 min-[2400px]:grid-cols-3">
		{#each v.parts as p (p.name)}
			{@const all = total(p.columns)}
			<section class="min-w-0 overflow-hidden rounded-xl border bg-card">
				<header class="space-y-2 border-b px-4 py-3">
					<div class="flex flex-wrap items-center gap-2">
						<h2 class="text-base font-semibold">{p.name}</h2>
						{#if p.stack}<Badge variant="secondary" class="num">{p.stack}</Badge>{/if}
						{#if p.dir}<span class="num flex items-center gap-1 text-xs text-muted-foreground"><FolderIcon class="size-3" />{p.dir}/</span>{/if}
						{#if p.needsOwner}<a href={href('home')} class="ml-auto rounded-md bg-st-needs/12 px-2 py-0.5 text-xs font-medium text-st-needs">{t('partNeeds', p.needsOwner)}</a>{/if}
					</div>
					{#if p.about}<p class="text-sm text-muted-foreground">{p.about}</p>{/if}
				</header>

				<div class="space-y-4 p-4">
					<!-- Work, by the board's columns. -->
					<div class="space-y-2">
						<div class="flex items-baseline justify-between text-xs">
							<span class="eyebrow text-muted-foreground">{t('partWork')}</span>
							<span class="num text-muted-foreground">{t('partOpen', p.open)}{p.lastIntegratedAt ? ' · ' + t('partLastIntegrated', ago(p.lastIntegratedAt)) : ''}</span>
						</div>
						{#if all}
							<div class="flex h-2 overflow-hidden rounded-full bg-muted">
								{#each COLUMNS.filter((c) => p.columns[c]) as c (c)}<div class={BG[COLUMN_TONE[c]]} style="width:{(p.columns[c] * 100) / all}%" title={columnLabel(c) + ': ' + p.columns[c]}></div>{/each}
							</div>
							<div class="num flex flex-wrap gap-x-3 gap-y-1 text-[11px]">
								{#each COLUMNS.filter((c) => p.columns[c]) as c (c)}<span class="flex items-center gap-1"><span class="size-1.5 rounded-full {BG[COLUMN_TONE[c]]}"></span><span class={TEXT[COLUMN_TONE[c]]}>{columnLabel(c)}</span> {p.columns[c]}</span>{/each}
							</div>
						{:else}<p class="text-xs text-muted-foreground">{t('partNoItems')}</p>{/if}
						{#if p.usage && Number(p.usage.tokens)}<p class="num text-[11px] text-muted-foreground">{t('partSpent', money(p.usage.costUsd), compact(p.usage.tokens), p.usage.sessions)}</p>{/if}
					</div>

					<!-- How each kind of item goes through this part. -->
					{#if Object.keys(p.flows).length}
						<div class="space-y-2">
							<p class="eyebrow text-muted-foreground">{t('partFlows')}</p>
							<ul class="space-y-1.5">
								{#each Object.entries(p.flows).sort() as [kind, flow] (kind)}
									<li class="flex flex-wrap items-center gap-1.5 text-xs">
										<Badge variant="outline" class="num">{kind}</Badge><ArrowRightIcon class="size-3 text-muted-foreground" />
										<span class="num font-medium">{flow}</span>
										{#if p.flowSteps[flow]}<span class="num text-muted-foreground">· {p.flowSteps[flow].steps.join(' → ')}</span>{/if}
									</li>
								{/each}
							</ul>
						</div>
					{/if}

					<div class="grid gap-4 sm:grid-cols-2">
						{#if p.checks.length}
							<div class="space-y-1.5">
								<p class="eyebrow text-muted-foreground">{t('partChecks')}</p>
								<div class="flex flex-wrap gap-1">{#each p.checks as c (c)}<code class="num rounded bg-muted px-1.5 py-0.5 text-[11px]">{c}</code>{/each}</div>
							</div>
						{/if}
						{#if p.paths.length}
							<div class="space-y-1.5">
								<p class="eyebrow text-muted-foreground">{t('partPaths')}</p>
								<div class="flex flex-wrap gap-1">{#each p.paths as x (x)}<code class="num rounded bg-muted px-1.5 py-0.5 text-[11px]">{x}</code>{/each}</div>
							</div>
						{/if}
					</div>
					{#if p.standard}<p class="num text-[11px] text-muted-foreground">{t('partStandard')}: {p.standard}</p>{/if}

					{#if p.config}
						<Collapsible.Root>
							<Collapsible.Trigger class="group flex w-full items-center gap-1.5 rounded-md border px-3 py-2 text-xs font-medium hover:bg-muted">
								<FileCodeIcon class="size-3.5" /><span class="num">{p.configFile}</span>
								<ChevronRightIcon class="ml-auto size-3.5 transition-transform group-data-[state=open]:rotate-90" />
							</Collapsible.Trigger>
							<Collapsible.Content><pre class="num mt-2 max-h-96 overflow-auto rounded-lg bg-muted/60 p-3 text-[11px] leading-relaxed">{p.config}</pre></Collapsible.Content>
						</Collapsible.Root>
					{:else if p.configFile}<p class="num text-[11px] text-muted-foreground">{p.configFile}</p>{/if}
				</div>
			</section>
		{/each}
	</div>

	{#if v.config}
		<div class="mt-4">
			<Section title={t('partProjectConfig')} sub={v.configFile}>
				<pre class="num max-h-[32rem] overflow-auto rounded-lg bg-muted/60 p-3 text-[11px] leading-relaxed">{v.config}</pre>
			</Section>
		</div>
	{/if}
</Loading>
