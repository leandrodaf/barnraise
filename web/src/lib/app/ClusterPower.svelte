<script lang="ts">
	import * as DropdownMenu from '$lib/components/ui/dropdown-menu';
	import PowerIcon from '@lucide/svelte/icons/power';
	import MoonStarIcon from '@lucide/svelte/icons/moon-star';
	import SunIcon from '@lucide/svelte/icons/sun';
	import ServerIcon from '@lucide/svelte/icons/server';
	import { t } from '$lib/i18n';
	import { cluster } from '$lib/cluster.svelte';
	import { session } from '$lib/session.svelte';
	import { go } from '$lib/router.svelte';
	import { standbyMachine, clusterStandby } from './actions';
	import { pendingStandby } from './standby.svelte';

	// The cluster's switch, always at hand: how many machines work, each one
	// a click from standby or back, and all of them at once.
	const machines = $derived((cluster.data?.machines || []).map((m) => m.machine!).filter((m) => m && !m.left).map((m) => ({ ...m, standby: pendingStandby.of(m) })));
	const resting = $derived(machines.filter((m) => m.standby).length);
	const working = $derived(machines.length - resting);
	const allResting = $derived(machines.length > 0 && resting === machines.length);
</script>

{#if machines.length}
	<DropdownMenu.Root>
		<DropdownMenu.Trigger>
			{#snippet child({ props })}
				<button {...props} title={t('clusterPowerTitle')}
					class="inline-flex h-8 items-center gap-1.5 rounded-md border px-2 text-xs font-medium transition-colors hover:bg-muted
						{allResting ? 'border-dashed border-st-needs/60 bg-st-needs/10 text-st-needs' : resting ? 'border-st-needs/40 text-foreground' : 'text-foreground'}">
					{#if allResting}<MoonStarIcon class="size-3.5" />{:else}<PowerIcon class="size-3.5 {resting ? 'text-st-needs' : 'text-st-done'}" />{/if}
					<span class="num whitespace-nowrap">{working}/{machines.length}</span>
					<span class="hidden whitespace-nowrap lg:inline">{t('clusterPowerWorking')}</span>
				</button>
			{/snippet}
		</DropdownMenu.Trigger>
		<DropdownMenu.Content align="end" class="w-72">
			<DropdownMenu.Label class="text-xs text-muted-foreground">{t('clusterPowerLabel', working, machines.length)}</DropdownMenu.Label>
			{#each machines as m (m.name)}
				<DropdownMenu.Item onSelect={() => standbyMachine(m.name, !m.standby)} class="gap-2.5">
					<span class="relative flex size-2 shrink-0">
						{#if !m.standby}<span class="absolute inline-flex size-full animate-ping rounded-full bg-st-done opacity-40"></span>{/if}
						<span class="relative inline-flex size-2 rounded-full {m.standby ? 'bg-st-needs' : 'bg-st-done'}"></span>
					</span>
					<span class="min-w-0 flex-1 truncate">{m.name}{#if m.name === session.machine}<span class="text-muted-foreground"> · {t('thisMachine')}</span>{/if}</span>
					<span class="shrink-0 text-[11px] {m.standby ? 'text-st-needs' : 'text-muted-foreground'}">{m.standby ? t('clusterPowerWake') : t('clusterPowerRest')}</span>
				</DropdownMenu.Item>
			{/each}
			<DropdownMenu.Separator />
			<DropdownMenu.Item disabled={allResting} onSelect={() => clusterStandby(true)}><MoonStarIcon />{t('allStandbyOn')}</DropdownMenu.Item>
			<DropdownMenu.Item disabled={!resting} onSelect={() => clusterStandby(false)}><SunIcon />{t('allStandbyOff')}</DropdownMenu.Item>
			<DropdownMenu.Separator />
			<DropdownMenu.Item onSelect={() => go('machines')}><ServerIcon />{t('clusterPowerOpen')}</DropdownMenu.Item>
		</DropdownMenu.Content>
	</DropdownMenu.Root>
{/if}
