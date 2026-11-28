<script lang="ts">
	// The conversation: one AI session per machine, about every project. The
	// panel, `barnraise chat` and the phone (the Claude app by its link, the
	// ChatGPT app once paired) show the same one; leaving the page leaves it
	// running.
	import { onDestroy, onMount } from 'svelte';
	import { toast } from 'svelte-sonner';
	import qrcode from 'qrcode-generator';
	import { conversation, type Conversation, type ConversationAccount } from '$lib/rest';
	import { t, tm } from '$lib/i18n';
	import { when } from '$lib/format';
	import { dialog } from '$lib/app/dialog.svelte';
	import PageHeader from '$lib/app/PageHeader.svelte';
	import ChatThread from '$lib/app/chat/ChatThread.svelte';
	import ChatComposer from '$lib/app/chat/ChatComposer.svelte';
	import type { ChatEntry } from '$lib/app/chat/chat';
	import { Button } from '$lib/components/ui/button';
	import { Switch } from '$lib/components/ui/switch';
	import { Spinner } from '$lib/components/ui/spinner';
	import MessagesIcon from '@lucide/svelte/icons/messages-square';
	import SparklesIcon from '@lucide/svelte/icons/sparkles';
	import SmartphoneIcon from '@lucide/svelte/icons/smartphone';
	import CopyIcon from '@lucide/svelte/icons/copy';
	import ExternalLinkIcon from '@lucide/svelte/icons/external-link';
	import PowerIcon from '@lucide/svelte/icons/power';
	import PlusIcon from '@lucide/svelte/icons/plus';
	import XIcon from '@lucide/svelte/icons/x';
	import CrosshairIcon from '@lucide/svelte/icons/crosshair';
	import TargetIcon from '@lucide/svelte/icons/target';
	import PackageIcon from '@lucide/svelte/icons/package-check';
	import OctagonIcon from '@lucide/svelte/icons/octagon-alert';
	import UserIcon from '@lucide/svelte/icons/circle-user-round';

	let view = $state<Conversation | null>(null);
	let raw = $state<Conversation['entries']>([]);
	let text = $state('');
	// The message sent and not yet in the transcript: shown at once.
	let pending = $state('');
	let sending = $state(false);
	let starting = $state(false);
	let switching = $state('');
	let failed = $state('');
	let timer: ReturnType<typeof setTimeout> | undefined;
	let alive = true;

	const running = $derived(!!view?.status.running);
	const busy = $derived(!!view?.status.busy || sending || !!pending || starting || !!switching);
	const remote = $derived(view?.status.remote_url || '');
	const accounts = $derived(view?.accounts || []);
	const current = $derived(accounts.find((a) => a.id === (view?.account || '')));
	const codex = $derived(current?.provider === 'codex');
	let pairing = $state<{ code: string; manual: string; expires: string } | null>(null);
	let pairBusy = $state(false);
	// What the QR code carries: the Claude link, or the ChatGPT pairing code.
	const qrText = $derived(codex ? pairing?.code || '' : remote);
	const qr = $derived.by(() => {
		if (!qrText) return { size: 0, d: '' };
		const q = qrcode(0, 'M');
		q.addData(qrText);
		q.make();
		const n = q.getModuleCount();
		let d = '';
		for (let r = 0; r < n; r++) for (let c = 0; c < n; c++) if (q.isDark(r, c)) d += `M${c + 2} ${r + 2}h1v1h-1z`;
		return { size: n + 4, d };
	});
	const entries = $derived<ChatEntry[]>(raw.map((e) => ({ ...e, key: String(e.seq) })));
	const working = $derived(starting || switching ? t('convStarting') : '');
	const draft = $derived(view?.status.busy ? view?.status.draft || '' : '');

	const SUGGEST = [
		{ k: 'convS1', icon: OctagonIcon },
		{ k: 'convS2', icon: PackageIcon },
		{ k: 'convS3', icon: CrosshairIcon },
		{ k: 'convS4', icon: TargetIcon }
	];
	const accountName = (a?: ConversationAccount) => (!a ? '' : a.email || (a.id && a.id !== 'codex' ? a.id : t('convOwnLogin')));

	async function load() {
		try {
			const v = await conversation.get(raw.length);
			if (!alive) return;
			failed = '';
			const switched = view && v.session_id !== view.session_id;
			view = v;
			if (switched) raw = (await conversation.get(0)).entries;
			else if (v.entries.length) {
				// A shorter transcript (a new session) starts the list over.
				if (v.entries[0].seq !== raw.length) raw = (await conversation.get(0)).entries;
				else raw = [...raw, ...v.entries];
			}
			if (pending && raw.some((e) => e.role === 'user' && e.text === pending)) pending = '';
		} catch (e) {
			failed = String((e as Error).message || e);
		}
		if (alive) timer = setTimeout(load, busy ? 900 : 3000);
	}
	function reload() {
		clearTimeout(timer);
		load();
	}

	// Tell the owner, once, that an answer arrived while they looked away:
	// when the answer is complete (no longer busy) and holds text that was
	// not there before. The working state flips at every tool an answer
	// runs; notifying on each flip sent several notices for one answer.
	let answered = -1;
	$effect(() => {
		const n = raw.filter((e) => e.role === 'assistant' && (e.text || '').trim()).length;
		if (busy) return;
		if (answered >= 0 && n > answered && document.hidden) {
			document.title = '● ' + t('convTitle') + ' · Barnraise';
			if ('Notification' in window && Notification.permission === 'granted') new Notification('Barnraise', { body: t('convAnswered'), tag: 'barnraise-conversation' });
		}
		answered = n;
	});
	function onVisible() {
		if (!document.hidden && document.title.startsWith('● ')) document.title = t('convTitle') + ' · Barnraise';
	}

	async function send(msg: string) {
		msg = msg.trim();
		if (!msg || sending) return;
		sending = true;
		pending = msg;
		text = '';
		if ('Notification' in window && Notification.permission === 'default') Notification.requestPermission().catch(() => {});
		try {
			if (!running) starting = true;
			await conversation.send(msg);
		} catch (e) {
			pending = '';
			text = msg;
			toast.error(t('error', (e as Error).message));
		} finally {
			sending = false;
			starting = false;
		}
		reload();
	}

	async function act(action: 'start' | 'stop' | 'interrupt' | 'keep' | 'release' | 'new') {
		try {
			if (action === 'start') starting = true;
			await conversation.act(action);
			if (action === 'interrupt') pending = '';
		} catch (e) {
			toast.error(t('error', (e as Error).message));
		} finally {
			starting = false;
		}
		reload();
	}

	async function pair() {
		pairBusy = true;
		try {
			pairing = await conversation.pair();
		} catch (e) {
			toast.error(t('error', (e as Error).message));
		} finally {
			pairBusy = false;
		}
	}

	async function use(a: ConversationAccount) {
		if (!a.usable || a.id === (view?.account || '') || switching) return;
		switching = a.id || '-';
		try {
			await conversation.use(a.id);
			raw = [];
			pending = '';
			pairing = null;
			toast.success(t('convSwitched', accountName(a)));
		} catch (e) {
			toast.error(t('error', (e as Error).message));
		} finally {
			switching = '';
		}
		reload();
	}

	function fresh() {
		dialog.open({
			title: t('convNewTitle'),
			description: t('convNewHelp'),
			fields: [],
			submit: t('convNew'),
			onSubmit: async () => {
				await conversation.act('new');
				raw = [];
				pending = '';
				reload();
			}
		});
	}

	async function copy(value: string) {
		await navigator.clipboard?.writeText(value);
		toast.success(t('convCopied'));
	}

	onMount(load);
	onDestroy(() => {
		alive = false;
		clearTimeout(timer);
	});
</script>

<svelte:document onvisibilitychange={onVisible} />

<PageHeader title={t('convTitle')} icon={MessagesIcon} meta={[view?.name, accountName(current)]} subtitle={t('convSub')}>
	{#snippet badges()}
		<span class="inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 text-xs font-medium {running ? 'border-st-done/30 bg-st-done/10 text-st-done' : 'text-muted-foreground'}">
			<span class="size-1.5 rounded-full {running ? (busy ? 'animate-pulse bg-st-review' : 'bg-st-done') : 'bg-muted-foreground/50'}"></span>
			{running ? (busy ? t('convAnswering') : t('convRunning')) : t('convStopped')}
		</span>
	{/snippet}
	{#snippet actions()}
		<Button size="sm" variant="outline" onclick={fresh} disabled={!view?.available}><PlusIcon />{t('convNew')}</Button>
		<Button size="sm" variant="ghost" onclick={() => act(running ? 'stop' : 'start')} disabled={!view?.available || starting} title={t('convShutHelp')}>
			<PowerIcon class={running ? 'text-st-done' : 'text-muted-foreground'} />{running ? t('convPowerOff') : t('convPowerOn')}
		</Button>
	{/snippet}
</PageHeader>

{#if view && !view.available}
	<p class="rounded-xl border bg-muted/40 p-4 text-sm text-muted-foreground">{t('convUnavailable')}: {view.why}</p>
{:else}
	<div class="grid min-h-0 gap-4 xl:grid-cols-[minmax(0,1fr)_18rem]">
		<section class="relative flex h-[calc(100dvh-12.5rem)] min-h-[30rem] flex-col overflow-hidden rounded-2xl border bg-card/30">
			<ChatThread {entries} {busy} {working} {draft} {pending} onreply={send}>
				{#snippet empty()}
					<div class="flex flex-col items-center gap-6 py-10 text-center">
						<span class="grid size-14 place-items-center rounded-2xl bg-gradient-to-br from-st-review/25 to-st-work/20 text-st-review shadow-sm ring-1 ring-st-review/20"><SparklesIcon class="size-7" /></span>
						<div class="space-y-1.5">
							<p class="text-lg font-semibold tracking-tight">{t('convHello')}</p>
							<p class="max-w-md text-sm text-muted-foreground">{t('convEmpty')}</p>
						</div>
						<div class="grid w-full max-w-xl gap-2 sm:grid-cols-2">
							{#each SUGGEST as s (s.k)}
								<button class="group flex items-start gap-3 rounded-xl border bg-card p-3 text-left text-sm transition hover:-translate-y-px hover:border-st-review/40 hover:shadow-sm disabled:opacity-60" onclick={() => send(t(s.k))} disabled={sending}>
									<s.icon class="mt-0.5 size-4 shrink-0 text-muted-foreground group-hover:text-st-review" />
									<span>{t(s.k)}</span>
								</button>
							{/each}
						</div>
					</div>
				{/snippet}
				{#snippet after()}
					{#if !busy && !running && view?.status.error}
						<p class="flex items-start gap-2 rounded-xl bg-st-fail/10 px-3 py-2 text-xs text-st-fail [overflow-wrap:anywhere]"><XIcon class="mt-px size-3.5 shrink-0" />{t('convEnded')}: {view.status.error}</p>
					{/if}
				{/snippet}
			</ChatThread>
			<ChatComposer bind:text onsend={send} onstop={() => act('interrupt')} busy={!!view?.status.busy} {sending} placeholder={t('convPlaceholder')}>
				{#snippet chip()}
					{#if current}
						<span class="inline-flex max-w-[60%] items-center gap-1.5 truncate rounded-full bg-muted px-2.5 py-1 text-[11px] text-muted-foreground" title={t('convAccountHelp')}><UserIcon class="size-3 shrink-0" /><span class="truncate">{accountName(current)}</span></span>
					{/if}
				{/snippet}
				{#snippet footer()}
					<p class="mx-auto mt-2 max-w-3xl text-center text-[11px] text-muted-foreground">{t('convPolicy')}</p>
				{/snippet}
			</ChatComposer>
		</section>

		<aside class="space-y-4">
			<div class="space-y-2 rounded-2xl border bg-card/40 p-4">
				<h2 class="flex items-center gap-2 text-sm font-medium"><UserIcon class="size-4 text-st-review" />{t('convAccount')}</h2>
				<p class="text-xs text-muted-foreground">{t('convAccountHelp')}</p>
				<div class="space-y-1.5 pt-1" role="radiogroup" aria-label={t('convAccount')}>
					{#each accounts as a (a.id)}
						{@const on = a.id === (view?.account || '')}
						<button role="radio" aria-checked={on} disabled={!a.usable || !!switching} onclick={() => use(a)} title={a.why || a.email || a.id}
							class="flex w-full items-start gap-2.5 rounded-xl border p-2.5 text-left transition {on ? 'border-st-review/50 bg-st-review/10' : 'hover:bg-muted/60'} disabled:cursor-not-allowed {a.usable ? '' : 'opacity-55'}">
							<span class="mt-0.5 grid size-4 shrink-0 place-items-center rounded-full border {on ? 'border-st-review bg-st-review' : ''}">{#if on}<span class="size-1.5 rounded-full bg-background"></span>{/if}</span>
							<span class="min-w-0 flex-1 space-y-0.5">
								<span class="flex items-center gap-1.5 text-xs font-medium">
									<span class="truncate">{a.email || (a.id && a.id !== 'codex' ? a.id : t('convOwnLogin'))}</span>
									{#if switching === (a.id || '-')}<Spinner class="size-3 shrink-0" />{/if}
								</span>
								<span class="block truncate text-[11px] text-muted-foreground">
									{a.provider === 'codex' ? 'ChatGPT · Codex' : 'Claude'} · {a.id && a.id !== 'codex' ? a.id : t('convOwnLogin')}{#if a.id && a.id !== 'codex' && a.usable} · {t('convWeek', Math.round(a.week_pct))}{/if}
								</span>
								{#if a.id && a.id !== 'codex' && a.usable}
									<span class="mt-1 block h-1 overflow-hidden rounded-full bg-muted"><span class="block h-full rounded-full {a.week_pct > 85 ? 'bg-st-fail' : a.week_pct > 60 ? 'bg-st-needs' : 'bg-st-done'}" style="width:{Math.min(100, a.week_pct)}%"></span></span>
								{/if}
								{#if a.why}<span class="block text-[11px] text-muted-foreground">{a.why}</span>{/if}
							</span>
						</button>
					{/each}
				</div>
			</div>
			<div class="space-y-3 rounded-2xl border bg-card/40 p-4">
				<h2 class="flex items-center gap-2 text-sm font-medium"><SmartphoneIcon class="size-4 text-st-review" />{codex ? t('convPairTitle') : t('convPhone')}</h2>
				{#if codex}
					{@const rs = view?.status.remote || ''}
					{#if rs}
						<p class="inline-flex items-center gap-1.5 text-xs {rs === 'connected' ? 'text-st-done' : rs === 'errored' ? 'text-st-fail' : 'text-muted-foreground'}">
							<span class="size-1.5 rounded-full {rs === 'connected' ? 'bg-st-done' : rs === 'errored' ? 'bg-st-fail' : 'animate-pulse bg-muted-foreground'}"></span>{tm('convRemote', rs)}
						</p>
					{/if}
					<p class="text-xs text-muted-foreground">{t('convPairHelp')}</p>
					{#if pairing}
						<svg viewBox="0 0 {qr.size} {qr.size}" class="mx-auto block w-36 rounded-xl bg-white shadow-sm ring-1 ring-border" shape-rendering="crispEdges" role="img" aria-label={t('convPairCode')}><path d={qr.d} fill="#000" /></svg>
						<div class="space-y-1 text-center">
							<p class="num text-2xl font-semibold tracking-[0.2em] select-all">{pairing.manual}</p>
							<p class="text-[11px] text-muted-foreground">{t('convPairUntil', when(pairing.expires))}</p>
						</div>
						<Button size="sm" variant="outline" class="w-full" onclick={() => copy(pairing?.manual || '')}><CopyIcon />{t('convCopy')}</Button>
					{:else}
						<Button size="sm" class="w-full" onclick={pair} disabled={pairBusy}>{#if pairBusy}<Spinner />{:else}<SmartphoneIcon />{/if}{t('convPairButton')}</Button>
					{/if}
				{:else if remote}
					<p class="text-xs text-muted-foreground">{t('convPhoneHelp')}</p>
					<svg viewBox="0 0 {qr.size} {qr.size}" class="mx-auto block w-40 rounded-xl bg-white shadow-sm ring-1 ring-border" shape-rendering="crispEdges" role="img" aria-label={t('convPhone')}><path d={qr.d} fill="#000" /></svg>
					<div class="flex gap-2">
						<Button size="sm" variant="outline" class="flex-1" onclick={() => copy(remote)}><CopyIcon />{t('convCopy')}</Button>
						<Button size="sm" variant="outline" href={remote} target="_blank" rel="noreferrer" aria-label={remote} title={remote}><ExternalLinkIcon /></Button>
					</div>
				{:else}
					<div class="grid h-32 place-items-center rounded-xl border border-dashed text-center text-xs text-muted-foreground">
						<span class="max-w-[12rem]">{running ? t('convPhoneConnecting') : t('convPhoneWait')}</span>
					</div>
				{/if}
			</div>
			<label class="flex cursor-pointer items-start gap-3 rounded-2xl border bg-card/40 p-4">
				<Switch checked={!!view?.keep} onCheckedChange={(on) => act(on ? 'keep' : 'release')} disabled={!view?.available} />
				<span class="space-y-1">
					<span class="block text-sm font-medium">{t('convKeep')}</span>
					<span class="block text-xs text-muted-foreground">{t('convKeepHelp')}</span>
				</span>
			</label>
			{#if failed}<p class="text-xs text-st-fail">{failed}</p>{/if}
		</aside>
	</div>
{/if}
