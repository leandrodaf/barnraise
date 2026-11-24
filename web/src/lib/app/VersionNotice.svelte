<script lang="ts">
	import * as Popover from '$lib/components/ui/popover';
	import { Button } from '$lib/components/ui/button';
	import ArrowUpCircleIcon from '@lucide/svelte/icons/circle-arrow-up';
	import RefreshIcon from '@lucide/svelte/icons/refresh-cw';
	import { t } from '$lib/i18n';
	import { when } from '$lib/format';
	import { session } from '$lib/session.svelte';

	// Two notices, both decided elsewhere: the daemon says a machine of the
	// cluster runs a newer build; the page sees the daemon answer with a build
	// other than the one it was loaded with.
</script>

{#if session.reloadNeeded}
	<button class="inline-flex h-8 items-center gap-1.5 rounded-md border border-st-work/50 bg-st-work/10 px-2 text-xs font-medium text-st-work hover:bg-st-work/20" onclick={() => location.reload()} title={t('reloadHelp', session.version)}>
		<RefreshIcon class="size-3.5 shrink-0" /><span class="hidden whitespace-nowrap sm:inline">{t('reloadNow')}</span>
	</button>
{:else if session.outdated}
	<Popover.Root>
		<Popover.Trigger>
			{#snippet child({ props })}
				<button {...props} class="inline-flex h-8 items-center gap-1.5 rounded-md border border-st-needs/50 bg-st-needs/10 px-2 text-xs font-medium text-st-needs hover:bg-st-needs/20">
					<ArrowUpCircleIcon class="size-3.5 shrink-0" /><span class="hidden whitespace-nowrap sm:inline">{t('updateAvailable')}</span>
				</button>
			{/snippet}
		</Popover.Trigger>
		<Popover.Content class="w-80 space-y-3 text-sm" align="end">
			<p class="font-medium">{t('updateTitle')}</p>
			<dl class="num grid grid-cols-[max-content_1fr] gap-x-3 gap-y-1 text-xs">
				<dt class="text-muted-foreground">{session.machine}</dt><dd>{session.version}{session.buildTime ? ' · ' + when(session.buildTime) : ''}</dd>
				<dt class="text-muted-foreground">{session.newestMachine}</dt><dd class="text-st-done">{session.newestVersion}{session.newestBuildTime ? ' · ' + when(session.newestBuildTime) : ''}</dd>
			</dl>
			<p class="text-xs text-muted-foreground">{session.selfUpdate ? t('updateSelf') : t('updateManual')}</p>
			{#if !session.selfUpdate}<code class="num block rounded bg-muted px-2 py-1.5 text-[11px]">make build && install -m755 barnraise ~/.local/bin/</code>{/if}
			<Button size="sm" variant="outline" class="w-full" onclick={() => session.readProjects()}>{t('checkAgain')}</Button>
		</Popover.Content>
	</Popover.Root>
{/if}
