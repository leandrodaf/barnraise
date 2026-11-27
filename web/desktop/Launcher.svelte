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
	import DownloadIcon from '@lucide/svelte/icons/download';
	import CheckIcon from '@lucide/svelte/icons/circle-check';
	import CircleIcon from '@lucide/svelte/icons/circle';
	import AlertIcon from '@lucide/svelte/icons/circle-alert';
	import { t } from '$lib/i18n';
	import LangSwitch from '$lib/app/LangSwitch.svelte';
	import '$lib/theme.svelte';
	import { inDesktop, install, installWSL, remote, startDaemon, status, type Local } from './bridge';

	// What the launcher is doing now; 'idle' waits for the owner.
	type Phase = 'looking' | 'installing' | 'starting' | 'opening' | 'idle';

	let local = $state<Local | null>(null);
	let phase = $state<Phase>('looking');
	let error = $state('');
	let base = $state('');
	let token = $state('');
	let elapsed = $state(0);
	let other = $state(false);
	let wslAsked = $state(false);
	// The launcher starts the daemon by itself once; after a failure it
	// waits for the owner, so it never loops on a daemon that cannot start.
	let autoStarted = false;

	const busy = $derived(phase !== 'idle');
	const wsl = $derived(local?.host === 'wsl');
	const installed = $derived(!!local && (!!local.binary || local.service));
	// With a daemon of its own this machine needs no address: the form for
	// another machine stays folded until it is asked for.
	const hasLocal = $derived(!!local && (local.running || installed || local.can_install || local.needs_wsl));

	const open = (u: string) => {
		phase = 'opening';
		window.location.replace(u);
	};

	// timed runs a long step with a running seconds count.
	async function timed<T>(p: Phase, f: () => Promise<T>): Promise<T> {
		phase = p;
		error = '';
		elapsed = 0;
		const began = Date.now();
		const tick = setInterval(() => (elapsed = Math.round((Date.now() - began) / 1000)), 500);
		try {
			return await f();
		} finally {
			clearInterval(tick);
		}
	}

	async function check(auto: boolean) {
		phase = 'looking';
		error = '';
		try {
			local = await status();
		} catch (e) {
			error = String(e);
			phase = 'idle';
			return;
		}
		// A daemon that answers with a token at hand: straight to its panel.
		if (local.running && local.panel_url) return open(local.panel_url);
		// Installed and stopped: start it without asking, once.
		if (auto && !autoStarted && !local.running && installed && !local.problem) {
			autoStarted = true;
			return start();
		}
		phase = 'idle';
	}

	async function start() {
		try {
			local = await timed('starting', startDaemon);
			if (local.panel_url) return open(local.panel_url);
		} catch (e) {
			error = String(e);
			local = await status().catch(() => local);
		}
		phase = 'idle';
	}

	async function doInstall() {
		try {
			local = await timed('installing', install);
		} catch (e) {
			error = String(e);
			phase = 'idle';
			return;
		}
		if (local.binary || local.service) return start();
		phase = 'idle';
	}

	async function doInstallWSL() {
		error = '';
		try {
			await installWSL();
			wslAsked = true;
		} catch (e) {
			error = String(e);
		}
	}

	async function connect(e: SubmitEvent) {
		e.preventDefault();
		phase = 'starting';
		error = '';
		try {
			open(await remote(base, token));
		} catch (err) {
			error = String(err);
			phase = 'idle';
		}
	}

	if (inDesktop()) check(true);
	else {
		error = t('dkNotDesktop');
		phase = 'idle';
	}
</script>

{#snippet step(done: boolean, working: boolean, label: string, detail: string)}
	<li class="flex items-start gap-3">
		<span class="mt-0.5 shrink-0">
			{#if done}<CheckIcon class="size-4 text-emerald-500" />
			{:else if working}<Spinner class="size-4" />
			{:else}<CircleIcon class="size-4 text-muted-foreground/50" />{/if}
		</span>
		<span class="min-w-0">
			<span class="block text-sm font-medium">{label}</span>
			{#if detail}<span class="block truncate text-xs text-muted-foreground" title={detail}>{detail}</span>{/if}
		</span>
	</li>
{/snippet}

<div class="relative grid min-h-svh place-items-center bg-muted/40 p-6">
	<div class="absolute top-4 right-4"><LangSwitch compact /></div>
	<Card.Root class="w-full max-w-md">
		<Card.Header>
			<BrandMark class="mb-2 size-10" />
			<Card.Title class="text-xl">Barnraise</Card.Title>
			<Card.Description>{wsl ? t('dkHereWSL') : t('dkIntro')}</Card.Description>
			{#if local}<p class="num pt-1 text-[11px] text-muted-foreground">{t('dkVersions', local.app_version, local.version || '—')}{#if wsl && local.distro}{' · ' + t('dkInWSL', local.distro)}{/if}</p>{/if}
		</Card.Header>
		<Card.Content class="space-y-5">
			{#if !local && phase === 'looking'}
				<div class="flex items-center gap-2 text-sm text-muted-foreground"><Spinner />{t('dkLooking')}</div>
			{:else if local?.needs_wsl}
				<div class="space-y-3 rounded-lg border p-4">
					<p class="flex items-center gap-2 text-sm font-medium"><AlertIcon class="size-4 text-amber-500" />{t('dkNeedsWSL')}</p>
					{#if local.problem}<p class="text-xs text-muted-foreground">{local.problem}</p>{/if}
					<div class="flex gap-2">
						<Button class="flex-1" disabled={busy || wslAsked} onclick={doInstallWSL}><DownloadIcon />{t('dkInstallWSL')}</Button>
						<Button variant="outline" size="icon" aria-label={t('retry')} disabled={busy} onclick={() => check(false)}><RefreshIcon /></Button>
					</div>
					<p class="text-xs text-muted-foreground">{t('dkInstallWSLAfter')}</p>
				</div>
			{:else if local}
				<div class="space-y-4 rounded-lg border p-4">
					<p class="text-sm font-medium">{t('dkThisMachine')}</p>
					<ol class="space-y-3">
						{@render step(installed, phase === 'installing', t('dkStepProgram'), installed ? (local.service ? t('dkViaService') : local.binary) : local.can_install ? (wsl && local.distro ? t('dkInstallWSLWhere', local.distro) : t('dkInstallWhy')) : t('dkNoBinary'))}
						{@render step(local.running, phase === 'starting', t('dkStepDaemon'), local.running ? t('dkRunning', local.machine, local.version, local.url) : phase === 'starting' ? t('dkStarting', elapsed) : t('dkStopped'))}
						{@render step(phase === 'opening', phase === 'opening', t('dkStepPanel'), phase === 'opening' ? t('dkOpening') : local.running && !local.has_token ? (wsl ? t('dkNoTokenWSL') : t('dkNoToken')) : '')}
					</ol>
					{#if local.problem}<p class="text-sm text-destructive">{local.problem}</p>{/if}
					{#if phase === 'starting'}
						<p class="text-xs text-muted-foreground" aria-live="polite">{t('dkStartingWhy')}</p>
					{:else if phase === 'installing'}
						<p class="text-xs text-muted-foreground" aria-live="polite">{t('dkInstalling', elapsed)}</p>
					{:else if phase === 'idle'}
						<div class="flex gap-2">
							{#if local.running && local.panel_url}
								<Button class="flex-1" onclick={() => open(local!.panel_url!)}>{t('dkOpen')}</Button>
							{:else if !installed && local.can_install}
								<Button class="flex-1" onclick={doInstall}><DownloadIcon />{t('dkInstall')}</Button>
							{:else if !local.running}
								<Button class="flex-1" disabled={!installed} onclick={start}><PlayIcon />{t('dkStart')}</Button>
							{/if}
							<Button variant="outline" size="icon" aria-label={t('retry')} onclick={() => check(false)}><RefreshIcon /></Button>
						</div>
						<p class="text-xs text-muted-foreground">{t('dkIndependent')}</p>
					{/if}
				</div>
			{/if}
			{#if error}
				<div class="space-y-1" role="alert">
					<p class="text-sm text-destructive">{error.split('\n')[0]}</p>
					{#if error.includes('\n')}
						<details class="text-xs text-muted-foreground">
							<summary class="cursor-pointer">{t('dkShowLog')}</summary>
							<pre class="mt-1 max-h-48 overflow-auto rounded bg-muted p-2 whitespace-pre-wrap">{error.split('\n').slice(1).join('\n')}</pre>
						</details>
					{/if}
				</div>
			{/if}
			{#if hasLocal && !other}
				<Button variant="ghost" size="sm" class="w-full text-muted-foreground" onclick={() => (other = true)}><GlobeIcon />{t('dkOtherShow')}</Button>
			{:else if local || error}
				<Separator />
				<form class="space-y-3" onsubmit={connect}>
					<p class="flex items-center gap-2 text-sm font-medium"><GlobeIcon class="size-4" />{t('dkOther')}</p>
					<p class="text-xs text-muted-foreground">{t('dkOtherWhy')}</p>
					<div class="grid gap-1.5"><Label for="base">{t('dkAddress')}</Label><Input id="base" placeholder="https://machine:7420" bind:value={base} required /></div>
					<div class="grid gap-1.5"><Label for="tok">{t('tokenLabel')}</Label><Input id="tok" type="password" bind:value={token} /></div>
					<Button type="submit" variant="outline" class="w-full" disabled={busy}>{t('dkConnect')}</Button>
				</form>
			{/if}
		</Card.Content>
	</Card.Root>
</div>
