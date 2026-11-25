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
	import SendIcon from '@lucide/svelte/icons/send';
	import { procs } from '$lib/procs.svelte';
	import { bytes, pct } from '$lib/format';
	import { messageAgent } from '$lib/app/ownerActions';

	// What each session's worktree runs on its machine (the CLI and what it started).
	procs.use();
	function res(machine: string, itemId: string) {
		const ws = (procs.data?.machines.find((m) => m.machine?.name === machine)?.worktrees || []).filter((w) => w.itemId === itemId && w.procs);
		if (!ws.length) return null;
		const kinds: Record<string, number> = {};
		for (const w of ws) for (const [k, n] of Object.entries(w.procs!.byKind)) kinds[k] = (kinds[k] || 0) + n;
		return { count: ws.reduce((s, w) => s + w.procs!.count, 0), cpu: ws.reduce((s, w) => s + w.procs!.cpuPct, 0), rss: ws.reduce((s, w) => s + w.procs!.rssBytes, 0n), kinds, inUse: ws.some((w) => w.inUse) };
	}

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
						{@const r = a.itemId ? res(a.machine, a.itemId) : null}
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
						{#if r}
							<div class="num grid grid-cols-3 gap-1.5 text-[11px]" title={Object.entries(r.kinds).map(([k, n]) => t('pk_' + k) + ' ' + n).join(' · ')}>
								<div class="rounded-md bg-muted/50 px-2 py-1"><p class="text-muted-foreground">CPU</p><p class="font-semibold">{pct(r.cpu)}</p></div>
								<div class="rounded-md bg-muted/50 px-2 py-1"><p class="text-muted-foreground">RAM</p><p class="font-semibold">{bytes(r.rss)}</p></div>
								<div class="rounded-md bg-muted/50 px-2 py-1"><p class="text-muted-foreground">{t('procCount')}</p><p class="font-semibold">{r.count}</p></div>
							</div>
						{/if}
						<footer class="num flex items-center justify-between gap-2 text-[11px] text-muted-foreground">
							<span>{a.since ? t('runningFor', since(a.since)) : since(a.at)}</span>
							<span class="flex items-center gap-2">{compact(a.tokens)} · {money(a.costUsd)}{#if a.itemId && !/^(done|failed)$/.test(a.state)}<button class="rounded p-0.5 hover:bg-muted hover:text-foreground" onclick={() => messageAgent(projectOf(a.itemId), a.itemId)} title={t('msgShort')} aria-label={t('msgShort')}><SendIcon class="size-3" /></button>{/if}</span>
						</footer>
					</article>
				{/each}
			</div>
		</section>
	{/each}
</div>
