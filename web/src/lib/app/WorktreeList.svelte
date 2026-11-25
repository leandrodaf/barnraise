<script lang="ts">
	import type { WorktreeInfo } from '$lib/gen/barnraise/v1/barnraise_pb';
	import GitBranchIcon from '@lucide/svelte/icons/git-branch';
	import { t } from '$lib/i18n';
	import { bytes, compact, pct, since } from '$lib/format';
	import { openItem } from './open';
	import Dot from './Dot.svelte';
	import Empty from './Empty.svelte';

	// Worktrees with what each one holds: branch, size, whether a session or a
	// check runs there, and its processes.
	let { list, machine = true }: { list: WorktreeInfo[]; machine?: boolean } = $props();
	const sorted = $derived([...list].sort((a, b) => Number(b.inUse) - Number(a.inUse) || (b.procs?.cpuPct || 0) - (a.procs?.cpuPct || 0) || Number(b.sizeBytes - a.sizeBytes)));
</script>

{#if !list.length}<div class="p-4"><Empty /></div>{:else}
	<ul class="divide-y">
		{#each sorted as w (w.machine + w.path)}
			<li class="num space-y-1 px-4 py-2.5 text-[11px]">
				<div class="flex flex-wrap items-center gap-x-2 gap-y-1">
					<Dot tone={w.inUse ? 'work' : 'idle'} pulse={w.inUse} />
					{#if w.itemId}<button class="font-medium hover:underline" onclick={() => openItem(w.project, w.itemId)}>{w.project}/{w.itemId}</button>{:else}<span class="font-medium">{w.project} · {w.name}</span>{/if}
					<span class="rounded bg-muted px-1 py-px text-[10px] text-muted-foreground">{t('wk_' + (w.kind || 'item'))}</span>
					{#if machine}<span class="text-muted-foreground">@ {w.machine}</span>{/if}
					{#if w.inUse}<span class="text-st-work">{t('wtInUse')}</span>{/if}
					{#if !w.setupDone}<span class="text-st-needs" title={t('wtSetupHelp')}>{t('wtSetupPending')}</span>{/if}
					<span class="ml-auto text-muted-foreground">{w.sizedAt ? bytes(w.sizeBytes) + ' · ' + t('wtFiles', compact(w.files)) : t('wtSizing')}</span>
				</div>
				<p class="flex items-center gap-1 truncate text-muted-foreground" title={w.path}>
					<GitBranchIcon class="size-3 shrink-0" />{w.branch || t('wtDetached')}{#if w.head}<span class="opacity-70">@{w.head.slice(0, 8)}</span>{/if}
					{#if w.modifiedAt}<span class="ml-auto shrink-0">{t('wtModified', since(w.modifiedAt))}</span>{/if}
				</p>
				{#if w.procs?.count}
					<p class="text-muted-foreground">{t('wtProcs', w.procs.count, pct(w.procs.cpuPct), bytes(w.procs.rssBytes))}</p>
				{/if}
			</li>
		{/each}
	</ul>
{/if}
