<script lang="ts">
	import BotIcon from '@lucide/svelte/icons/bot';
	import type { SessionInfo } from '$lib/gen/barnraise/v1/barnraise_pb';
	import { t } from '$lib/i18n';
	import { compact, money, parseTime, since } from '$lib/format';
	import { session } from '$lib/session.svelte';
	import { cluster } from '$lib/cluster.svelte';
	import { toneOf, TEXT } from '$lib/status';
	import { openItem } from '$lib/app/open';
	import PageHeader from '$lib/app/PageHeader.svelte';
	import BotIcon2 from '@lucide/svelte/icons/bot';
	import StatusBadge from '$lib/app/StatusBadge.svelte';
	import Dot from '$lib/app/Dot.svelte';
	import Empty from '$lib/app/Empty.svelte';

	// Live sessions of every machine; the stream of this page's project is
	// newer for its own agents. Finished sessions stay ten minutes.
	// Live sessions of the scope: the items the daemon put in it carry their
	// sessions; the stream of this page's projects is newer for its own agents.
	const projectOf = (itemId: string) => cluster.data?.projects.find((p) => p.items.some((i) => i.id === itemId))?.project || session.project;
	const byMachine = $derived.by(() => {
		const m = new Map<string, SessionInfo>();
		for (const p of cluster.data?.projects || []) for (const it of p.items) for (const s of it.sessions) m.set(s.agent, s);
		for (const [k, a] of session.agents) {
			const b = m.get(k);
			if (!b || (parseTime(a.at)?.getTime() || 0) >= (parseTime(b.at)?.getTime() || 0)) m.set(k, a);
		}
		const rows = [...m.values()].filter((a) => !/^(done|failed)$/.test(a.state) || Date.now() - (parseTime(a.at)?.getTime() || 0) < 600e3);
		const g = new Map<string, SessionInfo[]>();
		for (const r of rows) g.set(r.machine || '—', [...(g.get(r.machine || '—') || []), r]);
		return [...g.entries()].sort(([a], [b]) => a.localeCompare(b));
	});
	const total = $derived(byMachine.reduce((s, [, r]) => s + r.length, 0));
	
	const family = (provider: string) => (/claude|anthropic/i.test(provider) ? 'review' : /codex|openai|gpt/i.test(provider) ? 'done' : /gemini|google/i.test(provider) ? 'work' : 'idle');
</script>

<PageHeader title={t('agents')} icon={BotIcon2} meta={[session.scope || t('allProjects'), t('agentsSummary', total, byMachine.length)]} />
{#if !total}
	<div class="grid place-items-center rounded-xl border border-dashed py-16 text-center">
		<BotIcon class="mb-3 size-8 text-muted-foreground" />
		<p class="font-medium">{t('noAgents')}</p>
		<p class="mt-1 max-w-sm text-sm text-muted-foreground">{session.standby ? t('agentsStandby') : t('agentsIdle')}</p>
	</div>
{/if}
<div class="space-y-6">
	{#each byMachine as [machine, rows] (machine)}
		<section>
			<h2 class="eyebrow mb-2 flex items-center gap-2 text-muted-foreground"><Dot tone="done" />{machine}<span class="num">{rows.length}</span></h2>
			<div class="grid gap-3 md:grid-cols-2 xl:grid-cols-3 min-[1900px]:grid-cols-4 min-[2500px]:grid-cols-5">
				{#each rows as a (a.agent)}
					{@const tone = toneOf(a.state || 'idle')}
					<article class="space-y-3 rounded-xl border bg-card p-4">
						<header class="flex items-start justify-between gap-2">
							<div class="min-w-0">
								<p class="truncate font-medium">{a.agent}</p>
								<p class="num text-[11px] text-muted-foreground">{a.role}</p>
							</div>
							<StatusBadge status={a.state || 'idle'} map="agentStates" {tone} />
						</header>
						<div class="flex flex-wrap items-center gap-1.5 text-[11px]">
							<span class="num rounded-md px-1.5 py-0.5 {TEXT[family(a.provider) as 'idle']} bg-muted">{a.provider || '—'}</span>
							<span class="num rounded-md bg-muted px-1.5 py-0.5">{a.model}</span>
							{#if a.effort}<span class="num rounded-md bg-muted px-1.5 py-0.5">{a.effort}</span>{/if}
							<span class="num text-muted-foreground">{a.account}</span>
						</div>
						{#if a.itemId}
							<button class="w-full rounded-lg border bg-muted/30 px-3 py-2 text-left text-xs hover:bg-muted/60" onclick={() => openItem(projectOf(a.itemId), a.itemId)}>
								<span class="num text-muted-foreground">{a.itemId} · {a.step}</span>
								{#if a.detail}<p class="mt-0.5 line-clamp-2">{a.detail}</p>{/if}
							</button>
						{/if}
						<footer class="num flex justify-between text-[11px] text-muted-foreground">
							<span>{a.since ? t('runningFor', since(a.since)) : since(a.at)}</span>
							<span>{compact(a.tokens)} · {money(a.costUsd)}</span>
						</footer>
					</article>
				{/each}
			</div>
		</section>
	{/each}
</div>
