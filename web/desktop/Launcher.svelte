<script lang="ts">
	import BrandMark from '$lib/app/BrandMark.svelte';
	import * as Card from '$lib/components/ui/card';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Spinner } from '$lib/components/ui/spinner';
	import { Separator } from '$lib/components/ui/separator';
	import PlayIcon from '@lucide/svelte/icons/play';
	import GlobeIcon from '@lucide/svelte/icons/globe';
	import RefreshIcon from '@lucide/svelte/icons/refresh-cw';
	import { t } from '$lib/i18n';
	import LangSwitch from '$lib/app/LangSwitch.svelte';
	import '$lib/theme.svelte';
	import { inDesktop, remote, startDaemon, status, type Local } from './bridge';

	let local = $state<Local | null>(null);
	let busy = $state(false);
	let error = $state('');
	let base = $state('');
	let token = $state('');
	let elapsed = $state(0);
	let other = $state(false);
	// With a daemon of its own this machine needs no address: the form for
	// another machine stays folded until it is asked for.
	const hasLocal = $derived(!!local && (local.running || local.service || !!local.binary));

	const open = (u: string) => window.location.replace(u);

	async function check(auto: boolean) {
		error = '';
		try {
			local = await status();
			// A daemon that answers with a token at hand: straight to its panel.
			if (auto && local.running && local.panel_url) open(local.panel_url);
		} catch (e) {
			error = String(e);
		}
	}
	async function start() {
		busy = true;
		error = '';
		elapsed = 0;
		const began = Date.now();
		const tick = setInterval(() => (elapsed = Math.round((Date.now() - began) / 1000)), 500);
		try {
			local = await startDaemon();
			if (local.panel_url) open(local.panel_url);
		} catch (e) {
			error = String(e);
		} finally {
			clearInterval(tick);
			busy = false;
		}
	}
	async function connect(e: SubmitEvent) {
		e.preventDefault();
		busy = true;
		error = '';
		try {
			open(await remote(base, token));
		} catch (err) {
			error = String(err);
		} finally {
			busy = false;
		}
	}
	if (inDesktop()) check(true);
	else error = t('dkNotDesktop');
</script>

<div class="relative grid min-h-svh place-items-center bg-muted/40 p-6">
	<div class="absolute top-4 right-4"><LangSwitch compact /></div>
	<Card.Root class="w-full max-w-md">
		<Card.Header>
			<BrandMark class="mb-2 size-10" />
			<Card.Title class="text-xl">Barnraise</Card.Title>
			<Card.Description>{t('dkIntro')}</Card.Description>
			{#if local}<p class="num pt-1 text-[11px] text-muted-foreground">{t('dkVersions', local.app_version, local.version || '—')}</p>{/if}
		</Card.Header>
		<Card.Content class="space-y-5">
			{#if !local && !error}
				<div class="flex items-center gap-2 text-sm text-muted-foreground"><Spinner />{t('dkLooking')}</div>
			{:else if local}
				<div class="space-y-3 rounded-lg border p-4">
					<p class="text-sm font-medium">{t('dkThisMachine')}</p>
					{#if local.running}
						<p class="text-sm text-muted-foreground">{t('dkRunning', local.machine, local.version, local.url)}</p>
						{#if !local.has_token}<p class="text-sm text-destructive">{t('dkNoToken')}</p>{/if}
						{#if local.panel_url}<Button class="w-full" onclick={() => open(local!.panel_url!)}>{t('dkOpen')}</Button>{/if}
					{:else}
						<p class="text-sm text-muted-foreground">{local.problem || t('dkStopped')}</p>
						<p class="text-xs text-muted-foreground">{local.service ? t('dkViaService') : local.binary ? t('dkViaBinary', local.binary) : t('dkNoBinary')}</p>
						<div class="flex gap-2">
							<Button class="flex-1" disabled={busy || (!local.service && !local.binary)} onclick={start}>{#if busy}<Spinner />{t('dkStarting', elapsed)}{:else}<PlayIcon />{t('dkStart')}{/if}</Button>
							<Button variant="outline" size="icon" aria-label={t('retry')} onclick={() => check(false)}><RefreshIcon /></Button>
						</div>
						<p class="text-xs text-muted-foreground" aria-live="polite">{busy ? t('dkStartingWhy') : t('dkIndependent')}</p>
					{/if}
				</div>
			{/if}
			{#if hasLocal && !other}
				<Button variant="ghost" size="sm" class="w-full text-muted-foreground" onclick={() => (other = true)}><GlobeIcon />{t('dkOtherShow')}</Button>
			{:else}
				<Separator />
				<form class="space-y-3" onsubmit={connect}>
					<p class="flex items-center gap-2 text-sm font-medium"><GlobeIcon class="size-4" />{t('dkOther')}</p>
					<p class="text-xs text-muted-foreground">{t('dkOtherWhy')}</p>
					<div class="grid gap-1.5"><Label for="base">{t('dkAddress')}</Label><Input id="base" placeholder="https://machine:7420" bind:value={base} required /></div>
					<div class="grid gap-1.5"><Label for="tok">{t('tokenLabel')}</Label><Input id="tok" type="password" bind:value={token} /></div>
					<Button type="submit" variant="outline" class="w-full" disabled={busy}>{t('dkConnect')}</Button>
				</form>
			{/if}
			{#if error}<p class="text-sm text-destructive" role="alert">{error}</p>{/if}
		</Card.Content>
	</Card.Root>
</div>
