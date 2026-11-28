<script lang="ts">
	import { go, router } from '$lib/router.svelte';
	import * as Sheet from '$lib/components/ui/sheet';
	import { Button } from '$lib/components/ui/button';
	import { Textarea } from '$lib/components/ui/textarea';
	import { Spinner } from '$lib/components/ui/spinner';
	import SparklesIcon from '@lucide/svelte/icons/sparkles';
	import MaximizeIcon from '@lucide/svelte/icons/maximize-2';
	import MinimizeIcon from '@lucide/svelte/icons/minimize-2';
	import XIcon from '@lucide/svelte/icons/x';
	import CheckIcon from '@lucide/svelte/icons/check';
	import StarIcon from '@lucide/svelte/icons/star';
	import ChevronDownIcon from '@lucide/svelte/icons/chevron-down';
	import WandIcon from '@lucide/svelte/icons/wand-sparkles';
	import PencilIcon from '@lucide/svelte/icons/pencil';
	import MessageIcon from '@lucide/svelte/icons/message-circle-question';
	import FlaskIcon from '@lucide/svelte/icons/flask-conical';
	import type { DiscussionMessage, Proposal, Question } from '$lib/gen/barnraise/v1/barnraise_pb';
	import { toast } from 'svelte-sonner';
	import { api, errorText, store } from '$lib/api';
	import { t } from '$lib/i18n';
	import { ago, compact, dur } from '$lib/format';
	import { session } from '$lib/session.svelte';
	import { discussion } from './discussion.svelte';
	import { act } from './act';
	import { newItemByHand } from './actions';
	import { setFocus, setGoal } from './ownerActions';
	import { openItem } from './open';
	import PlusIcon from '@lucide/svelte/icons/plus';
	import ChatThread from './chat/ChatThread.svelte';
	import ChatComposer from './chat/ChatComposer.svelte';
	import type { ChatEntry } from './chat/chat';

	// The owner talks a decision over with the advisor. The daemon runs the
	// advisor (read-only, the strongest model), keeps the conversation and
	// tells, live, what the advisor reads while it prepares an answer.
	let messages = $state<DiscussionMessage[]>([]);
	let question = $state<Question | null>(null);
	let proposal = $state<Proposal | null>(null);
	let composer = $state<ReturnType<typeof ChatComposer> | undefined>();
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

	let revision = 0;
	async function load() {
		if (!discussion.open || !discussion.thread) return;
		try {
			const request = ++revision;
			const r = await api.getDiscussion({ project: discussion.project, thread: discussion.thread });
			if (request !== revision) return;
			if (!busy && r.messages.length >= messages.length) messages = r.messages;
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

	// The conversation as every chat in the panel shows it: the owner's
	// messages, the advisor's answers (model, tokens, delegated), the checks'
	// reports, and while it works what it reads and searches.
	const entries = $derived.by((): ChatEntry[] => {
		const out: ChatEntry[] = [];
		messages.forEach((m, i) => {
			const key = 'm' + i;
			if (m.role === 'owner') out.push({ key, role: 'user', text: m.text, at: m.at });
			else if (m.role === 'checks') out.push({ key, role: 'assistant', text: m.text, at: m.at, author: t('chatChecksBy') });
			else {
				const badges: ChatEntry['badges'] = [];
				if (m.delegated) badges.push({ label: t('delegatedBadge'), tone: 'review' });
				if (m.leftToOwner) badges.push({ label: t('leftToOwnerBadge'), tone: 'needs' });
				out.push({ key, role: 'assistant', text: m.text, at: m.at, author: t('discussAdvisor'), meta: m.model + (m.tokens && !m.delegated ? ' · ' + compact(m.tokens) + ' tokens' : ''), badges });
				if (m.delegated && m.suggestNote) out.push({ key: key + 'n', role: 'note', tone: 'muted', text: m.suggestNote, at: m.at });
			}
		});
		steps.forEach((st, i) => out.push({ key: 's' + i, role: 'tool', tool: st.tool, input: st.target, at: st.at }));
		return out;
	});

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
			revision++;
		} catch (err) {
			messages = messages.slice(0, -1);
			if (!text) text = v;
			if (!session.failed(err)) error = errorText(err);
		} finally {
			busy = false;
		}
	}
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
		composer?.focus();
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
		if (discussion.open && discussion.delegate && options.length && !busy && !session.advisorDown[discussion.project]) {
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
	// A conversation to ask for work (C-…) or to shape the goal (CG-…) or
	// the focus (CF-…): no decision on top; the item is written from it, or
	// the goal or the focus drafted for the owner to review, when the owner
	// is happy.
	const kind = $derived(discussion.thread.startsWith('CG-') ? 'goal' : discussion.thread.startsWith('CF-') ? 'focus' : discussion.thread.startsWith('C-') ? 'item' : '');
	const chat = $derived(kind !== '');
	const P = $derived(kind === 'goal' ? 'chatGoal' : kind === 'focus' ? 'chatFocus' : 'chat');
	const SUGGEST = $derived(chat ? [P + 'Idea1', P + 'Idea2', P + 'Idea3'] : ['discussWhat', 'discussRisk', 'discussRecommend']);
	let creating = $state(false);
	const answered = $derived(messages.some((m) => m.role === 'advisor'));
	// The project's checks, run by Barnraise on main at the owner's request
	// (never by the advisor): the result comes in as a message.
	const lastChecks = $derived(messages.findLast((m) => m.role === 'checks'));
	const checksRunning = $derived(!!lastChecks && lastChecks.text.startsWith('Running the checks'));
	let starting = $state(false);
	async function runChecks() {
		starting = true;
		error = '';
		try {
			await api.runChecksInChat({ project: discussion.project, thread: discussion.thread });
			await load();
		} catch (e) {
			if (!session.failed(e)) error = errorText(e);
		} finally {
			starting = false;
		}
	}
	async function createItem() {
		creating = true;
		error = '';
		try {
			const r = await api.createItemFromChat({ project: discussion.project, thread: discussion.thread });
			const id = r.item?.id || r.proposal?.id || '';
			toast.success(t('chatCreated', id));
			await load();
			session.bump(0);
			if (r.item?.id) openItem(discussion.project, r.item.id);
		} catch (e) {
			if (!session.failed(e)) error = errorText(e);
		} finally {
			creating = false;
		}
	}
	// The goal or the focus drafted from the conversation opens in its form:
	// the owner reviews it there and sets it. The conversation stays kept.
	async function draft() {
		creating = true;
		error = '';
		try {
			const r = await api.draftFromChat({ project: discussion.project, thread: discussion.thread });
			const project = discussion.project;
			discussion.open = false;
			if (kind === 'goal') setGoal(project, r.goal);
			else setFocus(project, r.focus, r.until);
		} catch (e) {
			if (!session.failed(e)) error = errorText(e);
		} finally {
			creating = false;
		}
	}
	function setOpen(open: boolean) {
		discussion.open = open;
		if (!open && router.current.name === 'queue' && router.current.args[1] === discussion.thread) go('queue');
	}
	function byHand() {
		const project = discussion.project;
		setOpen(false);
		if (kind === 'goal') setGoal(project);
		else if (kind === 'focus') setFocus(project);
		else newItemByHand();
	}
</script>

<Sheet.Root bind:open={() => discussion.open, setOpen}>
	<Sheet.Content side="right" class="flex !max-w-none flex-col gap-0 p-0 [&>button.absolute]:hidden" style="width:{shown}px">
		<!-- Drag the left edge to widen. -->
		<div class="absolute inset-y-0 -left-1 z-10 hidden w-2 cursor-col-resize hover:bg-st-review/30 sm:block" role="separator" aria-orientation="vertical" aria-label={t('discussResize')} onpointerdown={drag}></div>

		<header class="flex items-start gap-3 border-b px-4 py-3">
			<span class="mt-0.5 grid size-8 shrink-0 place-items-center rounded-lg bg-st-review/15 text-st-review"><SparklesIcon class="size-4" /></span>
			<div class="min-w-0 flex-1">
				<Sheet.Title class="text-base">{chat ? t(P + 'Title') : t('discussTitle')}</Sheet.Title>
				<Sheet.Description class="num truncate text-xs">{discussion.thread} · {discussion.project}</Sheet.Description>
			</div>
			<Button size="icon" variant="ghost" class="hidden size-8 sm:inline-flex" onclick={() => (wide = !wide)} aria-label={wide ? t('discussNarrow') : t('discussWiden')} title={wide ? t('discussNarrow') : t('discussWiden')}>
				{#if wide}<MinimizeIcon class="size-4" />{:else}<MaximizeIcon class="size-4" />{/if}
			</Button>
			<Button size="icon" variant="ghost" class="size-8" onclick={() => setOpen(false)} aria-label={t('close')}><XIcon class="size-4" /></Button>
		</header>

		<!-- The decision stays in view, and can be answered from here. -->
		<section class="border-b bg-muted/30 px-4 py-3 {chat ? 'hidden' : ''}">
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
						<Button size="sm" variant="secondary" class="h-auto min-h-8 py-1.5 sm:ml-auto" onclick={decideForMe} disabled={busy || !!session.advisorDown[discussion.project]} title={session.advisorDown[discussion.project] ? t('advisorDown', session.advisorDown[discussion.project]) : t('decideForMeHelp')}><WandIcon />{t('decideForMe')}</Button>
					</div>
				{/if}
			{/if}
		</section>

		{#if session.advisorDown[discussion.project]}
			<p class="border-b bg-st-needs/10 px-4 py-2 text-sm text-st-needs" role="status">{t('advisorDown', session.advisorDown[discussion.project])}</p>
		{/if}
		<ChatThread entries={entries} {busy} working={steps.length ? '' : t('discussThinking')} replies={false}>
			{#snippet empty()}
				<div class="space-y-3 text-sm text-muted-foreground">
					<p>{chat ? t(P + 'Intro', discussion.project) : t('discussIntro')}</p>
					<div class="flex flex-wrap gap-2">
						{#each SUGGEST as k (k)}<button class="rounded-full border bg-card px-3 py-1.5 text-xs text-foreground hover:bg-muted" onclick={() => { text = t(k); composer?.focus(); }}>{t(k)}</button>{/each}
					</div>
				</div>
			{/snippet}
			{#snippet after()}
				{#if (kind === 'goal' || kind === 'focus') && answered && !busy}
					<!-- The goal or the focus is drafted by a button, never by the advisor: say so where the eye is. -->
					<section class="flex flex-wrap items-center gap-3 rounded-xl border border-st-review/40 bg-st-review/[0.06] p-4">
						<p class="min-w-0 flex-1 text-sm text-muted-foreground">{t(P + 'Ready')}</p>
						<Button size="sm" disabled={creating || !!session.advisorDown[discussion.project]} onclick={draft}>
							{#if creating}<Spinner class="size-3.5" />{:else}<PencilIcon />{/if}{t(P + 'Draft')}
						</Button>
					</section>
				{/if}
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
				{#if error}<p class="rounded-md bg-st-fail/10 p-3 text-sm text-st-fail" role="alert">{t('error', error)}</p>{/if}
			{/snippet}
		</ChatThread>

		<ChatComposer bind:this={composer} bind:text onsend={() => send()} {busy} sending={busy} disabled={busy || !!session.advisorDown[discussion.project]} placeholder={t('discussPlaceholder')} hint={chat ? '' : t('discussFooter')} focusKey={false}>
			{#snippet chip()}
				{#if chat}<button type="button" class="shrink-0 rounded-full px-2 py-1 text-[11px] text-muted-foreground hover:bg-muted hover:text-foreground" onclick={byHand}>{t('chatByHand')}</button>{/if}
			{/snippet}
			{#snippet actions()}
				<Button type="button" size="sm" variant="ghost" class="h-8 rounded-full" disabled={starting || checksRunning} title={t('chatChecksHelp')} onclick={runChecks}>
					{#if starting || checksRunning}<Spinner class="size-3.5" />{:else}<FlaskIcon />{/if}<span class="hidden sm:inline">{checksRunning ? t('chatChecksRunning') : t('chatChecksRun')}</span>
				</Button>
				{#if chat}
					{#if kind === 'item'}
						<Button type="button" size="sm" variant="secondary" class="h-8 rounded-full" disabled={busy || creating || !answered || !!session.advisorDown[discussion.project]} title={answered ? t('chatCreateHelp') : t('chatCreateFirst')} onclick={createItem}>
							{#if creating}<Spinner class="size-3.5" />{:else}<PlusIcon />{/if}{t('chatCreate')}
						</Button>
					{:else}
						<Button type="button" size="sm" variant={answered ? 'default' : 'secondary'} class="h-8 rounded-full" disabled={busy || creating || !answered || !!session.advisorDown[discussion.project]} title={answered ? t('chatDraftHelp') : t('chatDraftFirst')} onclick={draft}>
							{#if creating}<Spinner class="size-3.5" />{:else}<PencilIcon />{/if}{t(P + 'Draft')}
						</Button>
					{/if}
				{/if}
			{/snippet}
		</ChatComposer>
	</Sheet.Content>
</Sheet.Root>
