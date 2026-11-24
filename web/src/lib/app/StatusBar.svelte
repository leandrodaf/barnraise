<script lang="ts">
	import MoonStarIcon from '@lucide/svelte/icons/moon-star';
	import { t } from '$lib/i18n';
	import { pct } from '$lib/format';
	import { href } from '$lib/router.svelte';
	import { session } from '$lib/session.svelte';
	import { cluster } from '$lib/cluster.svelte';
	import Dot from './Dot.svelte';

	const machines = $derived(cluster.all?.machines.map((m) => m.machine!).filter(Boolean) || []);
	// The account closest to its limit, of every project.
	// The account closest to its limit, as the daemon's outlook says it.
	const tight = $derived.by(() => {
		const all = (cluster.data?.projects || []).flatMap((p) => p.accounts);
		let best: { id: string; v: number; limited: boolean; short: boolean } | null = null;
		for (const a of all) {
			const o = a.outlook;
			const v = Math.max(o?.fiveHour?.usedPct || 0, o?.weekly?.usedPct || 0);
			if (!best || v > best.v) best = { id: a.id, v, limited: !!o?.limited, short: !!(o?.fiveHour?.runsOutIn || o?.weekly?.runsOutIn) };
		}
		return best;
	});
</script>

<footer class="num flex h-7 shrink-0 items-center gap-4 overflow-hidden border-t bg-sidebar px-3 text-[11px] whitespace-nowrap text-muted-foreground">
	<span class="flex items-center gap-1.5">
		{#key session.pulse}<Dot tone={session.live === 'on' ? 'done' : session.live === 'off' ? 'fail' : 'needs'} />{/key}
		{session.live === 'on' ? t('live') : session.live === 'off' ? t('offline', session.retryIn) : t('connecting')}
	</span>
	<span class="hidden sm:inline {session.outdated ? 'text-st-needs' : ''}">{t('sbDaemon', session.version, session.machine)}{session.outdated ? ' · ' + t('updateAvailable') : ''}</span>
	{#each machines as m (m.name)}
		<a href={href('machines')} class="hidden items-center gap-1 hover:text-foreground md:flex">
			{#if m.standby}<MoonStarIcon class="size-3 text-st-needs" />{:else}<Dot tone={m.health === 'green' ? 'done' : m.health === 'red' ? 'fail' : 'needs'} />{/if}{m.name} <span class="opacity-70">{m.busy}/{m.slots}</span>
		</a>
	{/each}
	{#if tight}<a href={href('accounts')} class="ml-auto truncate hover:text-foreground {tight.limited ? 'text-st-fail' : tight.short ? 'text-st-needs' : ''}">{t('sbTight', tight.id, pct(tight.v))}</a>{/if}
</footer>
