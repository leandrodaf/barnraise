<script lang="ts">
	import * as Sheet from '$lib/components/ui/sheet';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Spinner } from '$lib/components/ui/spinner';
	import KeyRoundIcon from '@lucide/svelte/icons/key-round';
	import ExternalLinkIcon from '@lucide/svelte/icons/external-link';
	import CopyIcon from '@lucide/svelte/icons/copy';
	import CheckIcon from '@lucide/svelte/icons/check';
	import ServerIcon from '@lucide/svelte/icons/server';
	import ChevronDownIcon from '@lucide/svelte/icons/chevron-down';
	import { untrack } from 'svelte';
	import type { AccountLogin } from '$lib/gen/barnraise/v1/barnraise_pb';
	import { toast } from 'svelte-sonner';
	import { api, errorText } from '$lib/api';
	import { t } from '$lib/i18n';
	import { session } from '$lib/session.svelte';
	import { cluster } from '$lib/cluster.svelte';
	import { loginPanel } from './login.svelte';
	import StatusBadge from './StatusBadge.svelte';

	// One account, every machine at once: each machine runs the provider's own
	// login and keeps it; this panel shows each one's link and code, and hands
	// back the code a page shows. The daemon decides everything; nothing here
	// touches a credential.
	const PROVIDERS = ['claude', 'codex'];
	let provider = $state('claude');
	let account = $state('');
	let picked = $state<Record<string, boolean>>({});
	let logins = $state<AccountLogin[]>([]);
	let codes = $state<Record<string, string>>({});
	let lines = $state<Record<string, boolean>>({});
	let busy = $state(false);
	let error = $state('');
	let copied = $state('');

	const machines = $derived((cluster.all?.machines || []).map((m) => m.machine?.name || '').filter(Boolean));
	const chosen = $derived(machines.filter((m) => picked[m] !== false));
	const running = $derived(logins.some((l) => l.state === 'running'));
	const done = $derived(logins.filter((l) => l.state === 'done').length);
	const tone = (s: string) => (s === 'done' ? 'done' : s === 'running' ? 'work' : s === 'cancelled' ? 'idle' : 'fail');

	// Opening: a new account starts clean; a known one shows the logins that
	// already run, so a panel closed by mistake picks them up again.
	$effect(() => {
		if (!loginPanel.open) return;
		account = loginPanel.account;
		provider = loginPanel.provider || 'claude';
		logins = [];
		codes = {};
		picked = {};
		error = '';
		// Only opening the panel starts over: what it does next is its own.
		if (loginPanel.account) untrack(read);
	});
	async function read() {
		if (!account) return;
		try {
			const r = await api.getAccountLogin({ account, all: true });
			if (r.account === account) logins = r.logins;
		} catch (e) {
			if (!session.failed(e)) error = errorText(e);
		}
	}
	$effect(() => {
		if (!loginPanel.open || !running) return;
		const id = setInterval(read, 1500);
		return () => clearInterval(id);
	});
	// When the last login ends, the screens read the accounts again.
	let wasRunning = false;
	$effect(() => {
		if (wasRunning && !running) session.bump(0);
		wasRunning = running;
	});

	async function start() {
		if (busy || !chosen.length) return;
		busy = true;
		error = '';
		try {
			const r = await api.startAccountLogin({ account, provider, machines: chosen });
			account = r.account;
			logins = r.logins;
		} catch (e) {
			if (!session.failed(e)) error = errorText(e);
		} finally {
			busy = false;
		}
	}
	async function sendCode(machine: string) {
		const code = (codes[machine] || '').trim();
		if (!code) return;
		try {
			await api.answerAccountLogin({ account, machines: [machine], code });
			codes[machine] = '';
			await read();
		} catch (e) {
			if (!session.failed(e)) toast.error(t('error', errorText(e)));
		}
	}
	async function cancel() {
		try {
			const r = await api.cancelAccountLogin({ account, all: true });
			logins = r.logins;
			await read();
		} catch (e) {
			if (!session.failed(e)) toast.error(t('error', errorText(e)));
		}
	}
	async function copy(key: string, s: string) {
		try {
			await navigator.clipboard.writeText(s);
			copied = key;
			setTimeout(() => (copied = ''), 1500);
		} catch {
			toast.error(t('copyFailed'));
		}
	}
</script>

<Sheet.Root bind:open={loginPanel.open}>
	<Sheet.Content side="right" class="flex w-full flex-col gap-0 p-0 sm:!max-w-xl">
		<header class="flex items-start gap-3 border-b px-4 py-3 pr-12">
			<span class="mt-0.5 grid size-8 shrink-0 place-items-center rounded-lg bg-st-work/15 text-st-work"><KeyRoundIcon class="size-4" /></span>
			<div class="min-w-0 flex-1">
				<Sheet.Title class="text-base">{account ? t('loginTitle', account) : t('loginNewTitle')}</Sheet.Title>
				<Sheet.Description class="text-xs">{t('loginHelp')}</Sheet.Description>
			</div>
		</header>

		<div class="min-h-0 flex-1 space-y-4 overflow-y-auto p-4">
			{#if !logins.length}
				{#if !account}
					<fieldset class="space-y-2">
						<legend class="eyebrow text-muted-foreground">{t('loginProvider')}</legend>
						<div class="flex flex-wrap gap-2">
							{#each PROVIDERS as p (p)}
								<Button size="sm" variant={provider === p ? 'default' : 'outline'} aria-pressed={provider === p} onclick={() => (provider = p)}>{p}</Button>
							{/each}
						</div>
						<p class="text-xs text-muted-foreground">{t('loginProviderHelp')}</p>
					</fieldset>
				{/if}
				<fieldset class="space-y-2">
					<legend class="eyebrow text-muted-foreground">{t('loginMachines')}</legend>
					{#if !machines.length}<p class="text-sm text-muted-foreground">{t('loading')}</p>{/if}
					<ul class="divide-y rounded-lg border">
						{#each machines as m (m)}
							<li>
								<label class="flex cursor-pointer items-center gap-3 px-3 py-2.5 text-sm">
									<input type="checkbox" class="size-4 accent-current" checked={picked[m] !== false} onchange={(e) => (picked[m] = e.currentTarget.checked)} />
									<ServerIcon class="size-3.5 text-muted-foreground" /><span class="num min-w-0 flex-1 truncate">{m}</span>
									{#if m === session.machine}<span class="text-xs text-muted-foreground">{t('loginThisMachine')}</span>{/if}
								</label>
							</li>
						{/each}
					</ul>
				</fieldset>
			{:else}
				<p class="text-sm text-muted-foreground">{running ? t('loginFollow') : t('loginEnded', done, logins.length)}</p>
				{#each logins as l (l.machine)}
					<section class="space-y-3 rounded-xl border p-4" aria-label={l.machine}>
						<header class="flex items-center gap-2">
							<ServerIcon class="size-4 text-muted-foreground" /><h3 class="num min-w-0 flex-1 truncate text-sm font-semibold">{l.machine}</h3>
							<StatusBadge status={l.state} tone={tone(l.state)} map="loginState" />
						</header>
						{#if l.state === 'running'}
							{#if l.url}
								<ol class="space-y-3 text-sm">
									<li class="space-y-2">
										<p>{t('loginStepOpen')}</p>
										<div class="flex flex-wrap gap-2">
											<Button size="sm" href={l.url} target="_blank" rel="noreferrer"><ExternalLinkIcon />{t('loginOpen')}</Button>
											<Button size="sm" variant="outline" onclick={() => copy(l.machine + 'u', l.url)}>{#if copied === l.machine + 'u'}<CheckIcon />{t('copied')}{:else}<CopyIcon />{t('loginCopyLink')}{/if}</Button>
										</div>
									</li>
									{#if l.code}
										<li class="space-y-2">
											<p>{t('loginStepType')}</p>
											<button class="num flex items-center gap-3 rounded-lg border bg-muted/40 px-4 py-2 text-xl font-semibold tracking-widest hover:bg-muted" onclick={() => copy(l.machine + 'c', l.code)} title={t('copy')}>
												{l.code}{#if copied === l.machine + 'c'}<CheckIcon class="size-4" />{:else}<CopyIcon class="size-4 text-muted-foreground" />{/if}
											</button>
										</li>
									{/if}
									{#if l.wantsCode}
										<li class="space-y-2">
											<p>{t('loginStepPaste')}</p>
											<form class="flex gap-2" onsubmit={(e) => (e.preventDefault(), sendCode(l.machine))}>
												<Input bind:value={codes[l.machine]} placeholder={t('loginCodePlaceholder')} autocomplete="off" spellcheck={false} aria-label={t('loginStepPaste')} class="num" />
												<Button type="submit" size="sm" class="h-9" disabled={!(codes[l.machine] || '').trim()}>{t('send')}</Button>
											</form>
										</li>
									{/if}
								</ol>
							{:else}<p class="text-sm text-muted-foreground">{t('loginStarting')}</p>{/if}
						{:else if l.state === 'done'}
							<p class="text-sm">{t('loginDone', account)}</p>
						{:else if l.error}
							<p class="rounded-md bg-st-fail/10 p-3 text-sm text-st-fail [overflow-wrap:anywhere]" role="alert">{l.error}</p>
						{/if}
						{#if l.lines.length}
							<button class="flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground" onclick={() => (lines[l.machine] = !lines[l.machine])}>
								<ChevronDownIcon class="size-3.5 transition-transform {lines[l.machine] ? '' : '-rotate-90'}" />{t('loginPrinted')}
							</button>
							{#if lines[l.machine]}<pre class="num max-h-48 overflow-auto rounded-md bg-muted/50 p-3 text-[11px] leading-relaxed whitespace-pre-wrap [overflow-wrap:anywhere]">{l.lines.join('\n')}</pre>{/if}
						{/if}
					</section>
				{/each}
			{/if}
			{#if error}<p class="rounded-md bg-st-fail/10 p-3 text-sm text-st-fail [overflow-wrap:anywhere]" role="alert">{t('error', error)}</p>{/if}
		</div>

		<footer class="flex flex-wrap items-center justify-between gap-2 border-t p-3">
			<p class="text-[11px] text-muted-foreground">{t('loginFooter')}</p>
			<div class="flex gap-2">
				{#if !logins.length}
					<Button size="sm" onclick={start} disabled={busy || !chosen.length}>{#if busy}<Spinner />{:else}<KeyRoundIcon />{/if}{t('loginStart', chosen.length)}</Button>
				{:else if running}
					<Button size="sm" variant="outline" onclick={cancel}>{t('loginCancel')}</Button>
				{:else}
					<Button size="sm" variant="outline" onclick={() => (logins = [])}>{t('loginAgain')}</Button>
					<Button size="sm" onclick={() => (loginPanel.open = false)}>{t('close')}</Button>
				{/if}
			</div>
		</footer>
	</Sheet.Content>
</Sheet.Root>
