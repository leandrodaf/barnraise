<script lang="ts">
	import KeyRoundIcon from '@lucide/svelte/icons/key-round';
	import KeyIcon from '@lucide/svelte/icons/key';
	import BanIcon from '@lucide/svelte/icons/ban';
	import GaugeIcon from '@lucide/svelte/icons/gauge';
	import TimerIcon from '@lucide/svelte/icons/timer-reset';
	import ServerIcon from '@lucide/svelte/icons/server';
	import LogInIcon from '@lucide/svelte/icons/log-in';
	import { Button } from '$lib/components/ui/button';
	import { loginPanel } from '$lib/app/login.svelte';
	import { t } from '$lib/i18n';
	import { ago, dur, num, pct } from '$lib/format';
	import { session } from '$lib/session.svelte';
	import { cluster } from '$lib/cluster.svelte';
	import { toneOf } from '$lib/status';
	import type { Account } from '$lib/gen/barnraise/v1/barnraise_pb';
	import PageHeader from '$lib/app/PageHeader.svelte';
	import PageGrid from '$lib/app/PageGrid.svelte';
	import Section from '$lib/app/Section.svelte';
	import Kpi from '$lib/app/Kpi.svelte';
	import StatusBadge from '$lib/app/StatusBadge.svelte';
	import PaceMeter from '$lib/app/PaceMeter.svelte';
	import Bars from '$lib/app/Bars.svelte';
	import Empty from '$lib/app/Empty.svelte';

	// Each account of the scope once (the daemon filters the scope); the
	// outlook of each one is the daemon's (domain.AccountOutlook).
	const accounts = $derived.by(() => {
		const by = new Map<string, Account>();
		for (const p of cluster.data?.projects || []) for (const a of p.accounts) if (!by.has(a.id)) by.set(a.id, a);
		const top = (a: Account) => Math.max(a.outlook?.fiveHour?.usedPct || 0, a.outlook?.weekly?.usedPct || 0);
		return [...by.values()].sort((a, b) => top(b) - top(a));
	});
	const limited = $derived(accounts.filter((a) => a.outlook?.limited));
	const free5h = $derived(accounts.length ? accounts.reduce((s, a) => s + (100 - (a.outlook?.fiveHour?.usedPct || 0)), 0) / accounts.length : 0);
	// The windows that reset next, of every account.
	const resets = $derived(
		accounts
			.flatMap((a) => [
				{ id: a.id, k: t('fiveHour'), w: a.outlook?.fiveHour },
				{ id: a.id, k: t('weekly'), w: a.outlook?.weekly }
			])
			.filter((r) => r.w?.resetsIn)
			.sort((a, b) => a.w!.resetsIn - b.w!.resetsIn)
	);
	// The providers whose login the daemon can run for the owner to follow.
	const LOGIN = ['claude', 'codex'];
	const count = (key: (a: Account) => string) => accounts.reduce((m, a) => ((m[key(a) || '—'] = (m[key(a) || '—'] || 0) + 1), m), {} as Record<string, number>);
</script>

<PageHeader title={t('accounts')} icon={KeyRoundIcon} meta={[session.scope || t('allProjects'), t('accountsN', accounts.length), limited.length ? t('limitedN', limited.length) : '']} subtitle={t('accountsHelp')}>
	{#snippet actions()}
		<Button size="sm" onclick={() => loginPanel.show()} title={t('loginHelp')}><LogInIcon />{t('loginEverywhere')}</Button>
	{/snippet}
</PageHeader>

{#if !cluster.data}<Empty text={t('loading')} />{:else}
	<div class="mb-4 grid grid-cols-2 gap-3 lg:grid-cols-4">
		<Kpi label={t('accounts')} value={accounts.length} tone="idle" icon={KeyIcon} hint={Object.keys(count((a) => a.provider)).join(', ')} />
		<Kpi label={t('kpiLimited')} value={limited.length} tone={limited.length ? 'fail' : 'done'} icon={BanIcon} hint={limited.map((a) => a.id).join(', ') || t('kpiNoneLimited')} />
		<Kpi label={t('kpiFree5h')} value={pct(free5h)} tone={free5h < 25 ? 'needs' : 'done'} icon={GaugeIcon} hint={t('kpiFree5hHint')} />
		<Kpi label={t('kpiNextReset')} value={resets[0] ? dur(resets[0].w!.resetsIn) : '—'} tone="work" icon={TimerIcon} hint={resets[0] ? resets[0].id + ' · ' + resets[0].k : ''} />
	</div>

	<PageGrid>
		<div class="grid gap-4 min-[1700px]:grid-cols-2">
		{#if !accounts.length}<Section title={t('accounts')}><Empty text={t('noAccounts')} /></Section>{/if}
		{#each accounts as a (a.id)}
			{@const u = a.usage}
			<Section title={a.id} sub={[a.provider, a.plan, a.machine, ...a.machines].filter(Boolean).join(' · ')}>
				{#snippet right()}{#if a.outlook?.limited}<StatusBadge status="limited" tone="fail" map="accountStatus" />{:else if a.status && a.status !== 'limited'}<StatusBadge status={a.status} tone={toneOf(a.status)} map="accountStatus" />{:else}<StatusBadge status="ok" tone="done" map="accountStatus" />{/if}{/snippet}
				<div class="grid gap-5 sm:grid-cols-2 2xl:grid-cols-3 min-[1700px]:grid-cols-2 min-[2300px]:grid-cols-3">
					<PaceMeter label={t('fiveHour')} w={a.outlook?.fiveHour} limited={a.outlook?.limited} />
					<PaceMeter label={t('weekly')} w={a.outlook?.weekly} limited={a.outlook?.limited} ceiling={a.ceilingPct < 100 ? a.ceilingPct : 0} />
					{#if a.outlook?.weeklyTop}<PaceMeter label={t('weeklyTop')} w={a.outlook.weeklyTop} limited={a.outlook?.limited} />{/if}
				</div>
				<p class="num mt-4 flex flex-wrap gap-x-3 gap-y-1 border-t pt-3 text-[11px] text-muted-foreground">
					<span class="flex items-center gap-1"><ServerIcon class="size-3" />{[a.machine, ...a.machines].filter(Boolean).join(', ') || t('anyMachine')}</span>
					{#if a.projects.length}<span>{t('projects')}: {a.projects.join(', ')}</span>{/if}
					{#if a.ceilingPct}<span>{t('ceilingLabel')} {num(a.ceilingPct)}%</span>{/if}
					{#if u?.updatedAt}<span class="sm:ml-auto">{t('usageFrom', u.source || '', ago(u.updatedAt))}</span>{/if}
				</p>
				{#if LOGIN.includes(a.provider)}
					<div class="mt-3 flex flex-wrap items-center gap-2">
						<Button size="sm" variant={a.status === 'expired' ? 'default' : 'outline'} onclick={() => loginPanel.show(a.id, a.provider)}><LogInIcon />{a.status === 'expired' ? t('loginRenew') : t('loginMore')}</Button>
					</div>
				{/if}
			</Section>
		{/each}
		</div>
		{#snippet aside()}
			<Section title={t('nextResets')} count={resets.length} pad={false}>
				{#if !resets.length}<div class="p-4"><Empty /></div>{:else}
					<ul class="divide-y">
						{#each resets.slice(0, 8) as r (r.id + r.k)}
							<li class="num flex items-center gap-3 px-4 py-2.5 text-xs">
								<span class="min-w-0 flex-1"><span class="font-medium text-foreground">{r.id}</span> <span class="text-muted-foreground">{r.k} · {pct(r.w!.usedPct)}</span></span>
								<span class="num shrink-0">{dur(r.w!.resetsIn)}</span>
							</li>
						{/each}
					</ul>
				{/if}
			</Section>
			<Section title={t('byMachine')}><Bars map={count((a) => a.machine)} fmt={num} /></Section>
			<Section title={t('byProvider')}><Bars map={count((a) => a.provider)} fmt={num} /></Section>
		{/snippet}
	</PageGrid>
{/if}
