<script lang="ts">
	import type { ProcRow } from '$lib/procs.svelte';
	import { kindTone } from '$lib/procs.svelte';
	import { t } from '$lib/i18n';
	import { bytes, dur, pct, since, when } from '$lib/format';
	import { SOFT } from '$lib/status';
	import { openItem } from './open';
	import Empty from './Empty.svelte';

	// Processes with where they run. `where` adds the machine and worktree columns.
	let { rows, where = true, limit = 0 }: { rows: ProcRow[]; where?: boolean; limit?: number } = $props();
	let sort = $state<'cpu' | 'rss' | 'time' | 'age'>('cpu');
	const shown = $derived.by(() => {
		const by = {
			cpu: (a: ProcRow, b: ProcRow) => b.cpuPct - a.cpuPct || Number(b.rssBytes - a.rssBytes),
			rss: (a: ProcRow, b: ProcRow) => Number(b.rssBytes - a.rssBytes),
			time: (a: ProcRow, b: ProcRow) => b.cpuSeconds - a.cpuSeconds,
			age: (a: ProcRow, b: ProcRow) => (a.startedAt || '').localeCompare(b.startedAt || '')
		}[sort];
		const s = [...rows].sort(by);
		return limit ? s.slice(0, limit) : s;
	});
	const maxCpu = $derived(Math.max(100, ...rows.map((r) => r.cpuPct)));
	const head = (k: typeof sort, label: string) => ({ k, label });
</script>

{#if !rows.length}
	<div class="p-4"><Empty text={t('procNone')} /></div>
{:else}
	<div class="overflow-x-auto">
		<table class="num w-full text-xs">
			<thead class="eyebrow border-b text-left text-muted-foreground">
				<tr>
					<th class="px-3 py-2 font-medium">PID</th>
					<th class="px-3 py-2 font-medium">{t('procKind')}</th>
					<th class="px-3 py-2 font-medium">{t('procCommand')}</th>
					{#if where}<th class="px-3 py-2 font-medium">{t('machine')}</th><th class="px-3 py-2 font-medium">{t('procWhere')}</th>{/if}
					{#each [head('cpu', 'CPU'), head('rss', 'RAM'), head('time', t('procCpuTime')), head('age', t('procAge'))] as h (h.k)}
						<th class="px-3 py-2 text-right font-medium"><button class="hover:text-foreground {sort === h.k ? 'text-foreground underline underline-offset-4' : ''}" onclick={() => (sort = h.k)}>{h.label}</button></th>
					{/each}
				</tr>
			</thead>
			<tbody class="divide-y">
				{#each shown as p (p.machine + p.pid + p.worktree)}
					<tr class="hover:bg-muted/40">
						<td class="px-3 py-1.5 text-muted-foreground">{p.pid}</td>
						<td class="px-3 py-1.5"><span class="rounded px-1.5 py-0.5 text-[10px] {SOFT[kindTone(p.kind)]}">{t('pk_' + (p.kind || 'other'))}</span></td>
						<td class="max-w-[28rem] px-3 py-1.5"><p class="truncate font-medium" title={p.command || p.name}>{p.command || p.name}</p></td>
						{#if where}
							<td class="px-3 py-1.5">{p.machine}</td>
							<td class="max-w-48 px-3 py-1.5">
								{#if p.itemId}<button class="truncate hover:underline" onclick={() => openItem(p.project, p.itemId)} title={p.path}>{p.project}/{p.itemId}</button>
								{:else}<span class="truncate text-muted-foreground" title={p.path}>{p.project} · {p.worktree}</span>{/if}
							</td>
						{/if}
						<td class="px-3 py-1.5 text-right">
							<span class="inline-flex items-center gap-1.5"><span class="hidden h-1 w-12 overflow-hidden rounded-full bg-muted sm:inline-block"><span class="block h-full bg-st-work" style="width:{Math.min(100, (p.cpuPct * 100) / maxCpu)}%"></span></span>{pct(p.cpuPct)}</span>
						</td>
						<td class="px-3 py-1.5 text-right">{bytes(p.rssBytes)}</td>
						<td class="px-3 py-1.5 text-right">{dur(p.cpuSeconds)}</td>
						<td class="px-3 py-1.5 text-right text-muted-foreground" title={when(p.startedAt)}>{since(p.startedAt)}</td>
					</tr>
				{/each}
			</tbody>
		</table>
	</div>
{/if}
