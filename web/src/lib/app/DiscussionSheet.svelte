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
	import WandIcon from '@lucide/svelte/icons/wand-sparkles';
	import PencilIcon from '@lucide/svelte/icons/pencil';
	import MessageIcon from '@lucide/svelte/icons/message-circle-question';
	import type { DiscussionMessage, Proposal, Question } from '$lib/gen/barnraise/v1/barnraise_pb';
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
	let proposal = $state<Proposal | null>(null);
	let input = $state<HTMLTextAreaElement | null>(null);
	let editing = $state(false);
	let editOption = $state('');
	let editNote = $state('');
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
			if (discussion.thread.startsWith('P-')) {
				const ps = await api.listProposals({ project: discussion.project });
				const all = ps.proposals.length ? ps.proposals : ps.groups.flatMap((g) => g.proposals);
				proposal = all.find((p) => p.id === discussion.thread) || null;
			} else proposal = null;
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
	// What can be answered from here: a question's options, or what the owner
	// does with a proposal. Nothing when the decision is already taken.
	const PROPOSAL = ['approve', 'reject', 'later'];
	const proposalLabel = (o: string) => t(o === 'approve' ? 'approveProposal' : o === 'reject' ? 'reject' : 'later');
	const options = $derived.by((): { value: string; label: string; recommended: boolean }[] => {
		if (question?.id === discussion.thread) return question.answer ? [] : question.options.map((o) => ({ value: o.label, label: o.label, recommended: o.recommended }));
		if (proposal?.id === discussion.thread && (!proposal.status || proposal.status === 'open' || proposal.status === 'later')) return PROPOSAL.map((o) => ({ value: o, label: proposalLabel(o), recommended: false }));
		return [];
	});
	const label = (value: string) => options.find((o) => o.value.toLowerCase() === value.toLowerCase())?.label || value;
	// The answer the advisor suggests: its last message, while the decision waits.
	const suggestion = $derived.by(() => {
		const last = messages[messages.length - 1];
		return !busy && options.length && last?.thread === discussion.thread && last?.role !== 'owner' && last?.suggestOption && !last.delegated ? last : null;
	});
	$effect(() => {
		void suggestion;
		editing = false;
	});

	async function answer(value: string, note = '') {
		const r = await act<unknown>(
			() =>
				question?.id === discussion.thread
					? api.answer({ project: discussion.project, id: question.id, answer: value, note })
					: api.decideProposal({ project: discussion.project, id: discussion.thread, decision: value.toLowerCase(), reason: note }),
			question ? t('answered', value) : t('decided', discussion.thread, label(value))
		);
		if (r) discussion.open = false;
	}
	function edit() {
		if (!suggestion) return;
		editOption = options.find((o) => o.value.toLowerCase() === suggestion.suggestOption.toLowerCase())?.value || options[0].value;
		editNote = suggestion.suggestNote;
		editing = true;
	}
	function askMore() {
		text = '';
		input?.focus();
	}
	// The advisor decides in the owner's place; the conversation keeps what
	// it chose and why, and the panel stays open to show it.
	async function decideForMe() {
		if (busy || !options.length) return;
		busy = true;
		started = Date.now();
		now = started;
		error = '';
		try {
			const r = await api.delegate({ project: discussion.project, thread: discussion.thread });
			messages = r.messages;
			toast.success(t('delegated', r.messages[r.messages.length - 1]?.suggestOption ? label(r.messages[r.messages.length - 1].suggestOption) : ''));
		} catch (err) {
			if (!session.failed(err)) error = errorText(err);
		} finally {
			busy = false;
			session.bump(0);
		}
	}
	// A screen asked for the advisor to decide: start once what is decided is known.
	$effect(() => {
		if (discussion.open && discussion.delegate && options.length && !busy) {
			discussion.delegate = false;
			decideForMe();
		}
	});
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
				<p class="mt-2 max-h-28 overflow-y-auto text-sm leading-relaxed [overflow-wrap:anywhere]">{question?.decision || proposal?.title || discussion.title}</p>
				{#if proposal?.problem}<p class="mt-1 max-h-20 overflow-y-auto text-xs leading-relaxed text-muted-foreground [overflow-wrap:anywhere]">{proposal.problem}</p>{/if}
				{#if options.length}
					<div class="mt-3 flex flex-wrap gap-2">
						{#each options as o (o.value)}
							<Button size="sm" variant={o.recommended ? 'default' : 'outline'} class="h-auto min-h-8 max-w-full py-1.5" onclick={() => answer(o.value)} disabled={busy}>
								<span class="whitespace-normal text-left">{o.label}</span>{#if o.recommended}<StarIcon class="size-3" />{/if}
							</Button>
						{/each}
						<Button size="sm" variant="secondary" class="h-auto min-h-8 py-1.5 sm:ml-auto" onclick={decideForMe} disabled={busy} title={t('decideForMeHelp')}><WandIcon />{t('decideForMe')}</Button>
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
								<span class="num">{m.model}{m.tokens && !m.delegated ? ' · ' + compact(m.tokens) + ' tokens' : ''} · {ago(m.at)}</span>
								{#if m.delegated}<span class="rounded-full bg-st-review/15 px-2 py-0.5 font-medium text-st-review">{t('delegatedBadge')}</span>{/if}
								{#if m.leftToOwner}<span class="rounded-full bg-st-needs/15 px-2 py-0.5 font-medium text-st-needs">{t('leftToOwnerBadge')}</span>{/if}
								<button class="ml-auto inline-flex items-center gap-1 rounded px-1.5 py-0.5 opacity-0 transition-opacity group-hover:opacity-100 hover:bg-muted focus:opacity-100" onclick={() => copy(i, m.text)}>
									{#if copied === i}<CheckIcon class="size-3" />{t('copied')}{:else}<CopyIcon class="size-3" />{t('copy')}{/if}
								</button>
							</header>
							<div class="[&_code]:[overflow-wrap:anywhere] [&_p]:text-[0.925rem] [&_p]:leading-7"><Markdown text={m.text} /></div>
							{#if m.delegated && m.suggestNote}<p class="rounded-md bg-muted/50 px-3 py-2 text-sm text-muted-foreground [overflow-wrap:anywhere]">{m.suggestNote}</p>{/if}
						</article>
					{/if}
				{/each}
				{#if suggestion}
					<!-- The answer the advisor suggests: confirm it, change it, or ask more. -->
					<section class="space-y-3 rounded-xl border border-st-review/40 bg-st-review/[0.06] p-4" aria-label={t('suggestTitle')}>
						<p class="eyebrow flex items-center gap-1.5 text-st-review"><SparklesIcon class="size-3.5" />{t('suggestTitle')}</p>
						{#if editing}
							<div class="flex flex-wrap gap-2">
								{#each options as o (o.value)}
									<Button size="sm" variant={editOption === o.value ? 'default' : 'outline'} class="h-auto min-h-8 max-w-full py-1.5" aria-pressed={editOption === o.value} onclick={() => (editOption = o.value)}>
										<span class="whitespace-normal text-left">{o.label}</span>
									</Button>
								{/each}
							</div>
							<Textarea bind:value={editNote} rows={4} aria-label={t('suggestNote')} placeholder={t('suggestNote')} />
							<div class="flex flex-wrap gap-2">
								<Button size="sm" onclick={() => answer(editOption, editNote.trim())}><CheckIcon />{t('suggestSend')}</Button>
								<Button size="sm" variant="ghost" onclick={() => (editing = false)}>{t('cancel')}</Button>
							</div>
						{:else}
							<p class="text-sm font-medium [overflow-wrap:anywhere]">{label(suggestion.suggestOption)}</p>
							{#if suggestion.suggestNote}<p class="text-sm leading-relaxed text-muted-foreground [overflow-wrap:anywhere]">{suggestion.suggestNote}</p>{/if}
							<div class="flex flex-wrap gap-2">
								<Button size="sm" onclick={() => answer(suggestion.suggestOption, suggestion.suggestNote)}><CheckIcon />{t('suggestAccept')}</Button>
								<Button size="sm" variant="outline" onclick={edit}><PencilIcon />{t('suggestEdit')}</Button>
								<Button size="sm" variant="ghost" onclick={askMore}><MessageIcon />{t('suggestAskMore')}</Button>
							</div>
						{/if}
					</section>
				{/if}
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
				<Textarea bind:ref={input} bind:value={text} rows={3} placeholder={t('discussPlaceholder')} onkeydown={keydown} disabled={busy} class="resize-none" />
				<div class="flex items-center justify-between gap-2">
					<p class="text-[11px] text-muted-foreground">{t('discussFooter')}</p>
					<Button type="submit" size="sm" disabled={busy || !text.trim()}><SendIcon />{t('send')}</Button>
				</div>
			</div>
		</form>
	</Sheet.Content>
</Sheet.Root>
