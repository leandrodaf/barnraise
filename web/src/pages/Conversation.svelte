<script lang="ts">
	// The conversation: one AI session per machine, about every project. The
	// panel, `barnraise chat` and the Claude app on the phone (Remote Control)
	// show the same one; leaving the page leaves it running.
	import { onDestroy, onMount, tick } from 'svelte';
	import { toast } from 'svelte-sonner';
	import qrcode from 'qrcode-generator';
	import { conversation, type Conversation, type ConversationAccount, type ConversationEntry } from '$lib/rest';
	import { t, tm } from '$lib/i18n';
	import { ago, when } from '$lib/format';
	import { dialog } from '$lib/app/dialog.svelte';
	import PageHeader from '$lib/app/PageHeader.svelte';
	import Markdown from '$lib/app/Markdown.svelte';
	import { Button } from '$lib/components/ui/button';
	import { Switch } from '$lib/components/ui/switch';
	import { Spinner } from '$lib/components/ui/spinner';
	import MessagesIcon from '@lucide/svelte/icons/messages-square';
	import SparklesIcon from '@lucide/svelte/icons/sparkles';
	import SendIcon from '@lucide/svelte/icons/arrow-up';
	import SquareIcon from '@lucide/svelte/icons/square';
	import SmartphoneIcon from '@lucide/svelte/icons/smartphone';
	import CopyIcon from '@lucide/svelte/icons/copy';
	import CheckIcon from '@lucide/svelte/icons/check';
	import ExternalLinkIcon from '@lucide/svelte/icons/external-link';
	import PowerIcon from '@lucide/svelte/icons/power';
	import PlusIcon from '@lucide/svelte/icons/plus';
	import ChevronRightIcon from '@lucide/svelte/icons/chevron-right';
	import ArrowDownIcon from '@lucide/svelte/icons/arrow-down';
	import TerminalIcon from '@lucide/svelte/icons/square-terminal';
	import FileIcon from '@lucide/svelte/icons/file-text';
	import SearchIcon from '@lucide/svelte/icons/search';
	import GlobeIcon from '@lucide/svelte/icons/globe';
	import PlugIcon from '@lucide/svelte/icons/plug';
	import ListChecksIcon from '@lucide/svelte/icons/list-checks';
	import WrenchIcon from '@lucide/svelte/icons/wrench';
	import XIcon from '@lucide/svelte/icons/x';
	import AlertIcon from '@lucide/svelte/icons/triangle-alert';
	import CrosshairIcon from '@lucide/svelte/icons/crosshair';
	import TargetIcon from '@lucide/svelte/icons/target';
	import PackageIcon from '@lucide/svelte/icons/package-check';
	import OctagonIcon from '@lucide/svelte/icons/octagon-alert';
	import UserIcon from '@lucide/svelte/icons/circle-user-round';

	let view = $state<Conversation | null>(null);
	let entries = $state<ConversationEntry[]>([]);
	let text = $state('');
	// The message sent and not yet in the transcript: shown at once.
	let pending = $state('');
	let sending = $state(false);
	let starting = $state(false);
	let switching = $state('');
	let failed = $state('');
	let copied = $state(-1);
	let list: HTMLDivElement | undefined = $state();
	let box: HTMLTextAreaElement | undefined = $state();
	let atBottom = $state(true);
	let timer: ReturnType<typeof setTimeout> | undefined;
	let alive = true;

	const running = $derived(!!view?.status.running);
	const busy = $derived(!!view?.status.busy || sending || !!pending);
	const remote = $derived(view?.status.remote_url || '');
	const accounts = $derived(view?.accounts || []);
	const current = $derived(accounts.find((a) => a.id === (view?.account || '')));
	const codex = $derived(current?.provider === 'codex');
	let pairing = $state<{ code: string; manual: string; expires: string } | null>(null);
	let pairBusy = $state(false);
	// What the QR code carries: the Claude link, or the ChatGPT pairing code.
	const qrText = $derived(codex ? pairing?.code || '' : remote);
	// The phone link as a QR code: one path of dark squares, drawn here.
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

	// Turns: the owner's message, then everything the assistant did and said
	// until the next one. Tool runs between two texts fold into one group.
	type Part = { kind: 'text'; e: ConversationEntry } | { kind: 'tools'; list: ConversationEntry[]; key: number };
	type Turn = { kind: 'user'; e: ConversationEntry } | { kind: 'assistant'; parts: Part[]; key: number; at?: string };
	const turns = $derived.by(() => {
		const out: Turn[] = [];
		for (const e of entries) {
			if (e.role === 'user') {
				out.push({ kind: 'user', e });
				continue;
			}
			let turn = out[out.length - 1];
			if (turn?.kind !== 'assistant') out.push((turn = { kind: 'assistant', parts: [], key: e.seq, at: e.at }));
			if (e.role === 'tool') {
				const last = turn.parts[turn.parts.length - 1];
				if (last?.kind === 'tools') last.list.push(e);
				else turn.parts.push({ kind: 'tools', list: [e], key: e.seq });
			} else turn.parts.push({ kind: 'text', e });
		}
		return out;
	});
	// What it is doing now: the last tool without a result, while busy.
	const now = $derived.by(() => {
		if (!busy) return null;
		const last = entries[entries.length - 1];
		return last?.role === 'tool' && !last.result ? last : null;
	});

	const SUGGEST = [
		{ k: 'convS1', icon: OctagonIcon },
		{ k: 'convS2', icon: PackageIcon },
		{ k: 'convS3', icon: CrosshairIcon },
		{ k: 'convS4', icon: TargetIcon }
	];

	// The tool's kind picks its icon and how it reads.
	function toolLook(name = '') {
		if (name === 'Bash') return { icon: TerminalIcon, label: 'Terminal' };
		if (name === 'Read' || name === 'LS') return { icon: FileIcon, label: t('convToolRead') };
		if (name === 'Grep' || name === 'Glob') return { icon: SearchIcon, label: t('convToolSearch') };
		if (name.startsWith('Web')) return { icon: GlobeIcon, label: 'Web' };
		if (name === 'TodoWrite') return { icon: ListChecksIcon, label: t('convToolPlan') };
		if (name.startsWith('mcp__')) return { icon: PlugIcon, label: name.split('__').slice(1).join(' · ') };
		return { icon: WrenchIcon, label: name };
	}
	const shortPath = (s = '') => s.replace(/^\/home\/[^/]+\//, '~/');
	const accountName = (a?: ConversationAccount) => (!a ? '' : a.email || (a.id ? a.id : t('convOwnLogin')));

	async function load() {
		try {
			const v = await conversation.get(entries.length);
			if (!alive) return;
			failed = '';
			const switched = view && v.session_id !== view.session_id;
			view = v;
			if (switched) entries = (await conversation.get(0)).entries;
			else if (v.entries.length) {
				// A shorter transcript (a new session) starts the list over.
				if (v.entries[0].seq !== entries.length) entries = (await conversation.get(0)).entries;
				else entries = [...entries, ...v.entries];
			}
			if (pending && entries.some((e) => e.role === 'user' && e.text === pending)) pending = '';
			if (atBottom) await bottom();
		} catch (e) {
			failed = String((e as Error).message || e);
		}
		if (alive) timer = setTimeout(load, busy ? 900 : 3000);
	}

	async function bottom(smooth = true) {
		await tick();
		list?.scrollTo({ top: list.scrollHeight, behavior: smooth ? 'smooth' : 'auto' });
	}

	function onScroll() {
		if (list) atBottom = list.scrollHeight - list.scrollTop - list.clientHeight < 80;
	}

	function reload() {
		clearTimeout(timer);
		load();
	}

	async function send(msg = text) {
		msg = msg.trim();
		if (!msg || sending) return;
		sending = true;
		pending = msg;
		text = '';
		atBottom = true;
		bottom();
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
			entries = [];
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
				entries = [];
				pending = '';
				reload();
			}
		});
	}

	async function copy(value: string, seq = -2) {
		await navigator.clipboard?.writeText(value);
		copied = seq;
		setTimeout(() => copied === seq && (copied = -1), 1500);
		if (seq === -2) toast.success(t('convCopied'));
	}
	const turnText = (turn: Turn) =>
		turn.kind === 'assistant' ? turn.parts.map((p) => (p.kind === 'text' ? p.e.text || '' : '')).filter(Boolean).join('\n\n') : '';

	function key(e: KeyboardEvent) {
		if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
			e.preventDefault();
			send();
		} else if (e.key === 'Escape' && view?.status.busy) {
			e.preventDefault();
			act('interrupt');
		}
	}

	// "/" anywhere puts the cursor in the message box.
	function onWindowKey(e: KeyboardEvent) {
		const el = e.target as HTMLElement;
		if (e.key === '/' && !e.metaKey && !e.ctrlKey && !/INPUT|TEXTAREA|SELECT/.test(el.tagName) && !el.isContentEditable) {
			e.preventDefault();
			box?.focus();
		}
	}

	onMount(() => {
		load().then(() => bottom(false));
		box?.focus();
	});
	onDestroy(() => {
		alive = false;
		clearTimeout(timer);
	});
</script>

<svelte:window onkeydown={onWindowKey} />

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
			<div bind:this={list} onscroll={onScroll} class="min-h-0 flex-1 overflow-y-auto" aria-live="polite">
				<div class="mx-auto w-full max-w-3xl space-y-7 px-4 pt-8 pb-6 sm:px-8">
					{#if !entries.length && !pending}
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
					{/if}

					{#each turns as turn, ti (turn.kind === 'user' ? 'u' + turn.e.seq : 'a' + turn.key)}
						{#if turn.kind === 'user'}
							<div class="group flex animate-[fade-in_.2s_ease-out] flex-col items-end gap-1">
								<div class="max-w-[85%] rounded-3xl rounded-br-lg bg-muted px-4 py-2.5 text-sm leading-relaxed whitespace-pre-wrap [overflow-wrap:anywhere]">{turn.e.text}</div>
								{#if turn.e.at}<span class="num px-2 text-[11px] text-muted-foreground opacity-0 transition group-hover:opacity-100" title={when(turn.e.at)}>{ago(turn.e.at)}</span>{/if}
							</div>
						{:else}
							<article class="group flex gap-3">
								<span class="mt-0.5 grid size-7 shrink-0 place-items-center rounded-full bg-st-review/15 text-st-review ring-1 ring-st-review/20"><SparklesIcon class="size-3.5" /></span>
								<div class="min-w-0 flex-1 space-y-3">
									{#each turn.parts as part (part.kind === 'text' ? 't' + part.e.seq : 'g' + part.key)}
										{#if part.kind === 'text'}
											<div class="text-sm leading-relaxed [&_pre]:max-h-96 [&_pre]:overflow-auto"><Markdown text={part.e.text || ''} /></div>
										{:else}
											{@const fails = part.list.filter((x) => x.failed).length}
											{@const live = part.list.some((x) => x === now)}
											<details class="group/tools rounded-xl border bg-background/50 text-xs" open={live}>
												<summary class="flex cursor-pointer list-none items-center gap-2 px-3 py-2 text-muted-foreground select-none hover:text-foreground">
													<ChevronRightIcon class="size-3.5 shrink-0 transition-transform group-open/tools:rotate-90" />
													{#if live}<Spinner class="size-3.5 text-st-review" />{:else if fails}<AlertIcon class="size-3.5 text-st-fail" />{:else}<CheckIcon class="size-3.5 text-st-done" />{/if}
													<span class="font-medium">{part.list.length === 1 ? t('convStep1') : t('convSteps', part.list.length)}</span>
													<span class="flex min-w-0 items-center gap-1 overflow-hidden">
														{#each part.list.slice(0, 8) as x (x.seq)}{@const L = toolLook(x.tool)}<L.icon class="size-3 shrink-0 opacity-70" />{/each}
													</span>
													{#if fails}<span class="ml-auto shrink-0 text-st-fail">{t('convFailedN', fails)}</span>{/if}
												</summary>
												<ol class="space-y-2.5 border-t px-3 py-3">
													{#each part.list as x (x.seq)}
														{@const L = toolLook(x.tool)}
														<li class="relative pl-6">
															<span class="absolute top-0 left-0 grid size-4 place-items-center {x.failed ? 'text-st-fail' : 'text-muted-foreground'}"><L.icon class="size-3.5" /></span>
															<p class="flex items-baseline gap-2">
																<span class="shrink-0 font-medium {x.failed ? 'text-st-fail' : 'text-foreground'}">{L.label}</span>
																<code class="num min-w-0 truncate text-muted-foreground" title={x.input}>{shortPath(x.input)}</code>
																{#if x === now}<Spinner class="ml-auto size-3 shrink-0" />{/if}
															</p>
															{#if x.result}
																<details class="mt-1">
																	<summary class="cursor-pointer text-[11px] text-muted-foreground hover:text-foreground">{x.failed ? t('convFailed') : t('convOutput')}</summary>
																	<pre class="num mt-1 max-h-48 overflow-auto rounded-lg bg-muted/60 p-2 text-[11px] leading-snug whitespace-pre-wrap [overflow-wrap:anywhere]">{x.result}</pre>
																</details>
															{/if}
														</li>
													{/each}
												</ol>
											</details>
										{/if}
									{/each}
									{#if turnText(turn) && !(busy && ti === turns.length - 1)}
										<div class="flex items-center gap-1 text-muted-foreground opacity-0 transition group-hover:opacity-100 focus-within:opacity-100">
											<Button size="icon" variant="ghost" class="size-7" onclick={() => copy(turnText(turn), turn.key)} aria-label={t('convCopyAnswer')} title={t('convCopyAnswer')}>
												{#if copied === turn.key}<CheckIcon class="size-3.5 text-st-done" />{:else}<CopyIcon class="size-3.5" />{/if}
											</Button>
											{#if turn.at}<span class="num text-[11px]" title={when(turn.at)}>{ago(turn.at)}</span>{/if}
										</div>
									{/if}
								</div>
							</article>
						{/if}
					{/each}

					{#if pending && !entries.some((e) => e.role === 'user' && e.text === pending)}
						<div class="flex justify-end"><div class="max-w-[85%] rounded-3xl rounded-br-lg bg-muted px-4 py-2.5 text-sm whitespace-pre-wrap opacity-70 [overflow-wrap:anywhere]">{pending}</div></div>
					{/if}
					{#if busy || starting || switching}
						<div class="flex items-center gap-3 text-sm" role="status">
							<span class="grid size-7 place-items-center rounded-full bg-st-review/15 text-st-review"><SparklesIcon class="size-3.5 animate-pulse" /></span>
							<span class="truncate bg-gradient-to-r from-muted-foreground via-foreground to-muted-foreground bg-[length:200%_100%] bg-clip-text text-transparent [animation:shimmer_2.2s_linear_infinite]">
								{starting || switching ? t('convStarting') : now ? toolLook(now.tool).label + ' · ' + shortPath(now.input) : t('convThinking')}
							</span>
						</div>
					{:else if !running && view?.status.error}
						<p class="flex items-start gap-2 rounded-xl bg-st-fail/10 px-3 py-2 text-xs text-st-fail [overflow-wrap:anywhere]"><XIcon class="mt-px size-3.5 shrink-0" />{t('convEnded')}: {view.status.error}</p>
					{/if}
				</div>
			</div>

			{#if !atBottom}
				<button class="absolute bottom-32 left-1/2 grid size-8 -translate-x-1/2 place-items-center rounded-full border bg-background shadow-md transition hover:bg-muted" onclick={() => { atBottom = true; bottom(); }} aria-label={t('convToBottom')} title={t('convToBottom')}><ArrowDownIcon class="size-4" /></button>
			{/if}

			<form class="px-3 pb-3 sm:px-6" onsubmit={(e) => { e.preventDefault(); send(); }}>
				<div class="mx-auto max-w-3xl rounded-2xl border bg-background shadow-sm transition focus-within:border-st-review/40 focus-within:shadow-md focus-within:ring-2 focus-within:ring-st-review/10">
					<textarea bind:this={box} bind:value={text} onkeydown={key} rows="1" placeholder={t('convPlaceholder')} aria-label={t('convPlaceholder')}
						class="block max-h-56 min-h-12 w-full resize-none bg-transparent px-4 pt-3.5 pb-1 text-sm outline-none [field-sizing:content] placeholder:text-muted-foreground"></textarea>
					<div class="flex items-center gap-2 px-2 pb-2">
						{#if current}
							<span class="inline-flex max-w-[60%] items-center gap-1.5 truncate rounded-full bg-muted px-2.5 py-1 text-[11px] text-muted-foreground" title={t('convAccountHelp')}><UserIcon class="size-3 shrink-0" /><span class="truncate">{accountName(current)}</span></span>
						{/if}
						<span class="hidden truncate pl-1 text-[11px] text-muted-foreground lg:block">{t('convKeys')}</span>
						<span class="ml-auto"></span>
						{#if view?.status.busy}
							<Button type="button" size="icon" variant="outline" class="size-8 rounded-full" onclick={() => act('interrupt')} title={t('convStop')} aria-label={t('convStop')}><SquareIcon class="size-3 fill-current" /></Button>
						{/if}
						<Button type="submit" size="icon" class="size-8 rounded-full" disabled={!text.trim() || sending} title={t('convSend')} aria-label={t('convSend')}>
							{#if sending}<Spinner class="size-4" />{:else}<SendIcon class="size-4" />{/if}
						</Button>
					</div>
				</div>
				<p class="mx-auto mt-2 max-w-3xl text-center text-[11px] text-muted-foreground">{t('convPolicy')}</p>
			</form>
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
