<script lang="ts">
	import * as Sheet from '$lib/components/ui/sheet';
	import { Button } from '$lib/components/ui/button';
	import { Textarea } from '$lib/components/ui/textarea';
	import { Spinner } from '$lib/components/ui/spinner';
	import SendIcon from '@lucide/svelte/icons/send-horizontal';
	import SparklesIcon from '@lucide/svelte/icons/sparkles';
	import MaximizeIcon from '@lucide/svelte/icons/maximize-2';
	import MinimizeIcon from '@lucide/svelte/icons/minimize-2';
	import XIcon from '@lucide/svelte/icons/x';
	import CopyIcon from '@lucide/svelte/icons/copy';
	import CheckIcon from '@lucide/svelte/icons/check';
	import StarIcon from '@lucide/svelte/icons/star';
	import FileTextIcon from '@lucide/svelte/icons/file-text';
	import SearchIcon from '@lucide/svelte/icons/search';
	import FolderSearchIcon from '@lucide/svelte/icons/folder-search';
	import ChevronDownIcon from '@lucide/svelte/icons/chevron-down';
	import type { DiscussionMessage, Question } from '$lib/gen/barnraise/v1/barnraise_pb';
	import { toast } from 'svelte-sonner';
	import { api, errorText, store } from '$lib/api';
	import { t } from '$lib/i18n';
	import { ago, compact, dur } from '$lib/format';
	import { session } from '$lib/session.svelte';
	import { discussion } from './discussion.svelte';
	import { act } from './act';
	import Markdown from './Markdown.svelte';

	// The owner talks a decision over with the advisor. The daemon runs the
	// advisor (read-only, the strongest model), keeps the conversation and
	// tells, live, what the advisor reads while it prepares an answer.
	let messages = $state<DiscussionMessage[]>([]);
	let question = $state<Question | null>(null);
	let text = $state('');
	let busy = $state(false);
	let error = $state('');
	let list = $state<HTMLElement | null>(null);
	let started = $state(0);
	let now = $state(Date.now());
	let showDecision = $state(true);
	let copied = $state(-1);

	// Width: dragged from the left edge or expanded; kept in this browser.
	const MIN = 380;
	let width = $state(Number(store.get('discussWidth')) || 640);
	let wide = $state(false);
	const maxWidth = () => window.innerWidth - 48;
	const shown = $derived(Math.min(wide ? maxWidth() : width, maxWidth()));
	function drag(e: PointerEvent) {
		e.preventDefault();
		wide = false;
		const move = (ev: PointerEvent) => (width = Math.max(MIN, Math.min(maxWidth(), window.innerWidth - ev.clientX)));
		const up = () => {
			window.removeEventListener('pointermove', move);
			window.removeEventListener('pointerup', up);
			store.set('discussWidth', String(Math.round(width)));
		};
		window.addEventListener('pointermove', move);
		window.addEventListener('pointerup', up);
	}

	async function load() {
		if (!discussion.open || !discussion.thread) return;
		try {
			const r = await api.getDiscussion({ project: discussion.project, thread: discussion.thread });
			if (!busy) messages = r.messages;
			if (discussion.thread.startsWith('Q-')) {
				const qs = await api.listQuestions({ project: discussion.project });
				question = qs.questions.find((q) => q.id === discussion.thread) || null;
			} else question = null;
		} catch (e) {
			if (!session.failed(e)) error = errorText(e);
		}
	}
	$effect(() => {
		void session.tick;
		void discussion.thread;
		if (discussion.open) load();
	});
	$effect(() => {
		void messages.length;
		void busy;
		void steps.length;
		queueMicrotask(() => list?.scrollTo({ top: list.scrollHeight, behavior: 'smooth' }));
	});
	$effect(() => {
		if (!busy) return;
		const id = setInterval(() => (now = Date.now()), 1000);
		return () => clearInterval(id);
	});

	// The advisor's actions for this conversation since the owner sent.
	const steps = $derived(busy ? session.advisorSteps.filter((s) => s.thread === discussion.thread && Date.parse(s.at) >= started - 2000) : []);

	async function send(e?: Event) {
		e?.preventDefault();
		const v = text.trim();
		if (!v || busy) return;
		busy = true;
		started = Date.now();
		now = started;
		error = '';
		messages = [...messages, { thread: discussion.thread, role: 'owner', text: v, at: new Date().toISOString() } as DiscussionMessage];
		text = '';
		try {
			const r = await api.discuss({ project: discussion.project, thread: discussion.thread, text: v });
			messages = r.messages;
		} catch (err) {
			if (!session.failed(err)) error = errorText(err);
		} finally {
			busy = false;
		}
	}
	const keydown = (e: KeyboardEvent) => {
		if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) send(e);
	};
	async function answer(label: string) {
		if (!question) return;
		const r = await act(() => api.answer({ project: discussion.project, id: question!.id, answer: label }), t('answered', label));
		if (r) discussion.open = false;
	}
	async function copy(i: number, s: string) {
		try {
			await navigator.clipboard.writeText(s);
			copied = i;
			setTimeout(() => (copied = -1), 1500);
		} catch {
			toast.error(t('copyFailed'));
		}
	}
	const SUGGEST = ['discussWhat', 'discussRisk', 'discussRecommend'];
	const stepIcon = (tool: string) => (tool === 'read' ? FileTextIcon : tool === 'search' ? SearchIcon : FolderSearchIcon);
</script>

<Sheet.Root bind:open={discussion.open}>
	<Sheet.Content side="right" class="flex !max-w-none flex-col gap-0 p-0 [&>button.absolute]:hidden" style="width:{shown}px">
		<!-- Drag the left edge to widen. -->
		<div class="absolute inset-y-0 -left-1 z-10 hidden w-2 cursor-col-resize hover:bg-st-review/30 sm:block" role="separator" aria-orientation="vertical" aria-label={t('discussResize')} onpointerdown={drag}></div>

		<header class="flex items-start gap-3 border-b px-4 py-3">
			<span class="mt-0.5 grid size-8 shrink-0 place-items-center rounded-lg bg-st-review/15 text-st-review"><SparklesIcon class="size-4" /></span>
			<div class="min-w-0 flex-1">
				<Sheet.Title class="text-base">{t('discussTitle')}</Sheet.Title>
				<Sheet.Description class="num truncate text-xs">{discussion.thread} · {discussion.project}</Sheet.Description>
			</div>
			<Button size="icon" variant="ghost" class="hidden size-8 sm:inline-flex" onclick={() => (wide = !wide)} aria-label={wide ? t('discussNarrow') : t('discussWiden')} title={wide ? t('discussNarrow') : t('discussWiden')}>
				{#if wide}<MinimizeIcon class="size-4" />{:else}<MaximizeIcon class="size-4" />{/if}
			</Button>
			<Button size="icon" variant="ghost" class="size-8" onclick={() => (discussion.open = false)} aria-label={t('close')}><XIcon class="size-4" /></Button>
		</header>

		<!-- The decision stays in view, and can be answered from here. -->
		<section class="border-b bg-muted/30 px-4 py-3">
			<button class="flex w-full items-center gap-2 text-left" onclick={() => (showDecision = !showDecision)}>
				<span class="eyebrow text-st-needs">{t('discussDecision')}</span>
				<ChevronDownIcon class="ml-auto size-4 text-muted-foreground transition-transform {showDecision ? '' : '-rotate-90'}" />
			</button>
			{#if showDecision}
				<p class="mt-2 max-h-28 overflow-y-auto text-sm leading-relaxed [overflow-wrap:anywhere]">{question?.decision || discussion.title}</p>
				{#if question?.options.length && !question.answer}
					<div class="mt-3 flex flex-wrap gap-2">
						{#each question.options as o (o.label)}
							<Button size="sm" variant={o.recommended ? 'default' : 'outline'} class="h-auto min-h-8 max-w-full py-1.5" onclick={() => answer(o.label)} disabled={busy}>
								<span class="whitespace-normal text-left">{o.label}</span>{#if o.recommended}<StarIcon class="size-3" />{/if}
							</Button>
						{/each}
					</div>
				{/if}
			{/if}
		</section>

		<div bind:this={list} class="min-h-0 flex-1 overflow-y-auto">
			<div class="mx-auto w-full max-w-3xl space-y-6 px-4 py-5 sm:px-6">
				{#if !messages.length && !busy}
					<div class="space-y-3 text-sm text-muted-foreground">
						<p>{t('discussIntro')}</p>
						<div class="flex flex-wrap gap-2">
							{#each SUGGEST as k (k)}<button class="rounded-full border bg-card px-3 py-1.5 text-xs text-foreground hover:bg-muted" onclick={() => (text = t(k))}>{t(k)}</button>{/each}
						</div>
					</div>
				{/if}
				{#each messages as m, i (i)}
					{#if m.role === 'owner'}
						<div class="flex justify-end">
							<div class="max-w-[85%] rounded-2xl rounded-br-md bg-muted px-4 py-2.5 text-sm whitespace-pre-wrap [overflow-wrap:anywhere]">{m.text}</div>
						</div>
					{:else}
						<article class="group space-y-2">
							<header class="flex items-center gap-2 text-xs text-muted-foreground">
								<SparklesIcon class="size-3.5 text-st-review" /><span class="font-medium text-foreground">{t('discussAdvisor')}</span>
								<span class="num">{m.model}{m.tokens ? ' · ' + compact(m.tokens) + ' tokens' : ''} · {ago(m.at)}</span>
								<button class="ml-auto inline-flex items-center gap-1 rounded px-1.5 py-0.5 opacity-0 transition-opacity group-hover:opacity-100 hover:bg-muted focus:opacity-100" onclick={() => copy(i, m.text)}>
									{#if copied === i}<CheckIcon class="size-3" />{t('copied')}{:else}<CopyIcon class="size-3" />{t('copy')}{/if}
								</button>
							</header>
							<div class="[&_code]:[overflow-wrap:anywhere] [&_p]:text-[0.925rem] [&_p]:leading-7"><Markdown text={m.text} /></div>
						</article>
					{/if}
				{/each}
				{#if busy}
					<div class="space-y-2 rounded-xl border border-dashed p-3">
						<p class="flex items-center gap-2 text-sm"><Spinner class="size-4 text-st-review" />{t('discussWorking')}<span class="num ml-auto text-xs text-muted-foreground">{dur((now - started) / 1000)}</span></p>
						{#if steps.length}
							<ul class="num space-y-1 text-[11px] text-muted-foreground">
								{#each steps.slice(-6) as s, i (i)}
									{@const Icon = stepIcon(s.tool)}
									<li class="flex min-w-0 items-center gap-1.5 {i === steps.slice(-6).length - 1 ? 'text-foreground' : ''}"><Icon class="size-3 shrink-0" /><span class="shrink-0">{t('step_' + s.tool)}</span><span class="truncate">{s.target}</span></li>
								{/each}
							</ul>
						{:else}<p class="text-xs text-muted-foreground">{t('discussThinking')}</p>{/if}
					</div>
				{/if}
				{#if error}<p class="rounded-md bg-st-fail/10 p-3 text-sm text-st-fail" role="alert">{t('error', error)}</p>{/if}
			</div>
		</div>

		<form class="border-t p-3" onsubmit={send}>
			<div class="mx-auto w-full max-w-3xl space-y-2">
				<Textarea bind:value={text} rows={3} placeholder={t('discussPlaceholder')} onkeydown={keydown} disabled={busy} class="resize-none" />
				<div class="flex items-center justify-between gap-2">
					<p class="text-[11px] text-muted-foreground">{t('discussFooter')}</p>
					<Button type="submit" size="sm" disabled={busy || !text.trim()}><SendIcon />{t('send')}</Button>
				</div>
			</div>
		</form>
	</Sheet.Content>
</Sheet.Root>
