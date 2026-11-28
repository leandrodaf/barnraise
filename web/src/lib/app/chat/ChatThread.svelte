<script lang="ts">
	// A conversation with an AI as the panel shows it everywhere: the owner's
	// messages, each answer with the steps (tools) that led to it folded and
	// live, the answer as it is written, one-tap replies when it asks
	// something, and what it does now while it works.
	import { tick, type Snippet } from 'svelte';
	import { t } from '$lib/i18n';
	import { ago, when } from '$lib/format';
	import Markdown from '$lib/app/Markdown.svelte';
	import { Button } from '$lib/components/ui/button';
	import { Spinner } from '$lib/components/ui/spinner';
	import SparklesIcon from '@lucide/svelte/icons/sparkles';
	import CopyIcon from '@lucide/svelte/icons/copy';
	import CheckIcon from '@lucide/svelte/icons/check';
	import ChevronRightIcon from '@lucide/svelte/icons/chevron-right';
	import ArrowDownIcon from '@lucide/svelte/icons/arrow-down';
	import AlertIcon from '@lucide/svelte/icons/triangle-alert';
	import { type ChatEntry, toTurns, turnText, asksOwner, toolLook, shortPath, clock } from './chat';

	let {
		entries,
		busy = false,
		working = '',
		draft = '',
		pending = '',
		replies = true,
		onreply,
		empty,
		after,
		class: cls = '',
		authorIcon = SparklesIcon
	}: {
		entries: ChatEntry[];
		// busy: an answer is under way; working says what it does now.
		busy?: boolean;
		working?: string;
		// draft: the answer as it is written; pending: the owner's message
		// sent and not yet back from the server.
		draft?: string;
		pending?: string;
		replies?: boolean;
		onreply?: (text: string) => void;
		empty?: Snippet;
		after?: Snippet;
		class?: string;
		authorIcon?: typeof SparklesIcon;
	} = $props();

	let list: HTMLDivElement | undefined = $state();
	let atBottom = $state(true);
	let copied = $state('');
	// What the owner opened stays open (and what they closed stays closed)
	// while new messages arrive: the page keeps it, not the DOM.
	let opened = $state<Record<string, boolean>>({});
	const isOpen = (key: string, auto: boolean) => opened[key] ?? auto;
	function toggled(key: string, e: Event) {
		const now = (e.currentTarget as HTMLDetailsElement).open;
		if (opened[key] !== now) opened = { ...opened, [key]: now };
	}

	const turns = $derived(toTurns(entries));
	// The step that runs now: the last tool with no result, while busy.
	const live = $derived.by(() => {
		if (!busy) return null;
		const last = entries[entries.length - 1];
		return last?.role === 'tool' && !last.result ? last : null;
	});
	const showPending = $derived(!!pending && !entries.some((e) => e.role === 'user' && e.text === pending));
	const asks = $derived(replies && !!onreply && !busy && !pending && asksOwner(turns));
	const REPLIES = ['convYes', 'convNo', 'convExplain'];

	// How long the answer under way has taken.
	let since = $state(0);
	let nowMs = $state(Date.now());
	$effect(() => {
		if (!busy) {
			since = 0;
			return;
		}
		if (!since) since = Date.now();
		const iv = setInterval(() => (nowMs = Date.now()), 1000);
		return () => clearInterval(iv);
	});
	const elapsed = $derived(since ? (nowMs - since) / 1000 : 0);

	// Follow what is new unless the owner scrolled up to read.
	export async function bottom(smooth = true) {
		await tick();
		list?.scrollTo({ top: list.scrollHeight, behavior: smooth ? 'smooth' : 'auto' });
	}
	// Only when the thread grows (a message, the answer being written), not
	// when the state flips between working and idle: the page stayed put
	// before, and jumped on every poll. While an answer streams in, the
	// scroll follows at once; a smooth scroll on every token kept the page
	// moving.
	let grown = 0;
	$effect(() => {
		const size = entries.length * 1e6 + draft.length + (showPending ? 1 : 0);
		if (size === grown) return;
		const first = grown === 0;
		grown = size;
		if (atBottom) bottom(!first && !draft);
	});
	function onScroll() {
		if (list) atBottom = list.scrollHeight - list.scrollTop - list.clientHeight < 80;
	}

	async function copy(text: string, key: string) {
		await navigator.clipboard?.writeText(text);
		copied = key;
		setTimeout(() => copied === key && (copied = ''), 1500);
	}
	const tone = { review: 'bg-st-review/15 text-st-review', needs: 'bg-st-needs/15 text-st-needs', fail: 'bg-st-fail/15 text-st-fail', done: 'bg-st-done/15 text-st-done' };
</script>

<div class="relative flex min-h-0 flex-1 flex-col {cls}">
	<div bind:this={list} onscroll={onScroll} class="min-h-0 flex-1 overflow-y-auto" aria-live="polite">
		<div class="mx-auto w-full max-w-3xl space-y-7 px-4 pt-6 pb-6 sm:px-8">
			{#if !entries.length && !pending && !busy}{@render empty?.()}{/if}

			{#each turns as turn, ti (turn.kind === 'user' ? 'u' + turn.e.key : 'a' + turn.key)}
				{#if turn.kind === 'user'}
					<div class="group flex flex-col items-end gap-1">
						<div class="max-w-[85%] rounded-3xl rounded-br-lg bg-muted px-4 py-2.5 text-sm leading-relaxed whitespace-pre-wrap [overflow-wrap:anywhere]">{turn.e.text}</div>
						{#if turn.e.at}<span class="num px-2 text-[11px] text-muted-foreground opacity-0 transition group-hover:opacity-100" title={when(turn.e.at)}>{turn.e.author ? turn.e.author + ' · ' : ''}{ago(turn.e.at)}</span>{/if}
					</div>
				{:else}
					{@const AuthorIcon = authorIcon}
					<article class="group flex gap-3">
						<span class="mt-0.5 grid size-7 shrink-0 place-items-center rounded-full bg-st-review/15 text-st-review ring-1 ring-st-review/20"><AuthorIcon class="size-3.5" /></span>
						<div class="min-w-0 flex-1 space-y-3">
							{#if turn.author || turn.meta || turn.badges?.length}
								<header class="-mb-1 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
									<span class="font-medium text-foreground">{turn.author || 'Barnraise'}</span>
									{#if turn.meta}<span class="num">{turn.meta}</span>{/if}
									{#each turn.badges || [] as b (b.label)}<span class="rounded-full px-2 py-0.5 font-medium {tone[b.tone]}">{b.label}</span>{/each}
								</header>
							{/if}
							{#each turn.parts as part (part.kind === 'tools' ? 'g' + part.key : part.kind + part.e.key)}
								{#if part.kind === 'text'}
									<div class="text-sm leading-relaxed [&_pre]:max-h-96 [&_pre]:overflow-auto"><Markdown text={part.e.text || ''} /></div>
								{:else if part.kind === 'note'}
									<p class="flex items-start gap-2 rounded-xl px-3 py-2 text-xs [overflow-wrap:anywhere] {part.e.tone === 'fail' ? 'bg-st-fail/10 text-st-fail' : 'bg-muted/50 text-muted-foreground'}">
										{#if part.e.tone === 'fail'}<AlertIcon class="mt-px size-3.5 shrink-0" />{/if}{part.e.text}
									</p>
								{:else}
									{@const fails = part.list.filter((x) => x.failed).length}
									{@const running = part.list.some((x) => x === live)}
									<details class="group/tools rounded-xl border bg-background/50 text-xs" open={isOpen('g' + part.key, running)} ontoggle={(e) => toggled('g' + part.key, e)}>
										<summary class="flex cursor-pointer list-none items-center gap-2 px-3 py-2 text-muted-foreground select-none hover:text-foreground">
											<ChevronRightIcon class="size-3.5 shrink-0 transition-transform group-open/tools:rotate-90" />
											{#if running}<Spinner class="size-3.5 text-st-review" />{:else if fails}<AlertIcon class="size-3.5 text-st-fail" />{:else}<CheckIcon class="size-3.5 text-st-done" />{/if}
											<span class="font-medium">{part.list.length === 1 ? t('convStep1') : t('convSteps', part.list.length)}</span>
											<span class="flex min-w-0 items-center gap-1 overflow-hidden">
												{#each part.list.slice(0, 10) as x (x.key)}{@const L = toolLook(x.tool)}<L.icon class="size-3 shrink-0 opacity-70" />{/each}
											</span>
											{#if fails}<span class="ml-auto shrink-0 text-st-fail">{t('convFailedN', fails)}</span>{/if}
										</summary>
										<ol class="space-y-2.5 border-t px-3 py-3">
											{#each part.list as x (x.key)}
												{@const L = toolLook(x.tool)}
												<li class="relative pl-6">
													<span class="absolute top-0 left-0 grid size-4 place-items-center {x.failed ? 'text-st-fail' : 'text-muted-foreground'}"><L.icon class="size-3.5" /></span>
													<p class="flex items-baseline gap-2">
														<span class="shrink-0 font-medium {x.failed ? 'text-st-fail' : 'text-foreground'}">{L.label}</span>
														<code class="num min-w-0 truncate text-muted-foreground" title={x.input}>{shortPath(x.input)}</code>
														{#if x === live}<Spinner class="ml-auto size-3 shrink-0" />{/if}
													</p>
													{#if x.result}
														<details class="mt-1" open={isOpen('r' + x.key, false)} ontoggle={(e) => toggled('r' + x.key, e)}>
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

			{#if draft}
				{@const AuthorIcon = authorIcon}
				<article class="flex gap-3">
					<span class="mt-0.5 grid size-7 shrink-0 place-items-center rounded-full bg-st-review/15 text-st-review ring-1 ring-st-review/20"><AuthorIcon class="size-3.5" /></span>
					<div class="min-w-0 flex-1 text-sm leading-relaxed after:ml-0.5 after:inline-block after:h-4 after:w-1.5 after:animate-pulse after:rounded-sm after:bg-st-review after:align-middle after:content-['']"><Markdown text={draft} /></div>
				</article>
			{/if}
			{#if asks}
				<div class="flex flex-wrap gap-2 pl-10">
					{#each REPLIES as k (k)}
						<button class="rounded-full border bg-card px-3 py-1.5 text-xs transition hover:-translate-y-px hover:border-st-review/40 hover:bg-st-review/10" onclick={() => onreply?.(t(k))}>{t(k)}</button>
					{/each}
				</div>
			{/if}
			{#if showPending}
				<!-- The message as sent, in the same place and look it keeps once the
				     server has it: the swap is not seen. -->
				<div class="flex flex-col items-end gap-1"><div class="max-w-[85%] rounded-3xl rounded-br-lg bg-muted px-4 py-2.5 text-sm leading-relaxed whitespace-pre-wrap [overflow-wrap:anywhere]">{pending}</div></div>
			{/if}
			{@render after?.()}
			{#if busy}
				<div class="flex items-center gap-3 text-sm" role="status">
					<span class="grid size-7 shrink-0 place-items-center rounded-full bg-st-review/15 text-st-review"><SparklesIcon class="size-3.5 animate-pulse" /></span>
					<span class="min-w-0 truncate bg-gradient-to-r from-muted-foreground via-foreground to-muted-foreground bg-[length:200%_100%] bg-clip-text text-transparent [animation:shimmer_2.2s_linear_infinite]">
						{working || (live ? toolLook(live.tool).label + ' · ' + shortPath(live.input) : draft ? t('convWriting') : t('convThinking'))}
					</span>
					{#if elapsed > 2}<span class="num shrink-0 text-xs text-muted-foreground">{clock(elapsed)}</span>{/if}
				</div>
			{/if}
		</div>
	</div>
	{#if !atBottom}
		<button class="absolute bottom-3 left-1/2 grid size-8 -translate-x-1/2 place-items-center rounded-full border bg-background shadow-md transition hover:bg-muted" onclick={() => { atBottom = true; bottom(); }} aria-label={t('convToBottom')} title={t('convToBottom')}><ArrowDownIcon class="size-4" /></button>
	{/if}
</div>
