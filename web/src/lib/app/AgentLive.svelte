<script lang="ts">
	// The Agent tab of an item: what the agent running on it writes and the
	// tools it runs, live, as the same conversation the owner has with
	// Barnraise, its messages with the owner in the same thread, and its
	// pulse: working or stuck. The stream opens with the tab and closes with
	// it (ADR-0048): nobody watching costs nothing.
	import { onDestroy, untrack } from 'svelte';
	import { Spinner } from '$lib/components/ui/spinner';
	import BotIcon from '@lucide/svelte/icons/bot';
	import { api, getToken } from '$lib/api';
	import { t } from '$lib/i18n';
	import { since } from '$lib/format';
	import ChatThread from '$lib/app/chat/ChatThread.svelte';
	import ChatComposer from '$lib/app/chat/ChatComposer.svelte';
	import AgentPulse, { type Pulse } from '$lib/app/chat/AgentPulse.svelte';
	import type { ChatEntry } from '$lib/app/chat/chat';

	// running: who runs on the item now (agent names), so a new session reopens the stream.
	let { project, id, running = '' }: { project: string; id: string; running?: string } = $props();

	type Line = { at: string; kind: string; tool?: string; text?: string; error?: boolean; since?: string; last?: string };
	type Info = { agent: string; role: string; step: string; provider: string; model: string; machine: string; since?: string };
	type Msg = { id: string; from: string; to: string; text: string; at: string };

	let phase = $state<'connecting' | 'live' | 'idle' | 'elsewhere' | 'ended' | 'error'>('connecting');
	let info = $state<Info | null>(null);
	let lines = $state<Line[]>([]);
	let msgs = $state<Msg[]>([]);
	let pulse = $state<Pulse | null>(null);
	let elsewhere = $state({ machine: '', why: '' });
	let failure = $state('');
	let text = $state('');
	let sending = $state(false);
	let pending = $state('');
	let ctrl: AbortController | null = null;
	const MAX_LINES = 400;

	function handle(event: string, data: any) {
		switch (event) {
			case 'session':
				info = data;
				phase = 'live';
				break;
			case 'line':
				if (data.kind === 'pulse') {
					pulse = data;
					break;
				}
				lines = lines.length >= MAX_LINES ? [...lines.slice(-MAX_LINES + 1), data] : [...lines, data];
				if (data.kind === 'end') pulse = null;
				break;
			case 'end':
				phase = 'ended';
				pulse = null;
				break;
			case 'idle':
				phase = 'idle';
				break;
			case 'elsewhere':
				elsewhere = { machine: data.machine || '', why: data.why || '' };
				phase = 'elsewhere';
				break;
			case 'message':
				if (!msgs.some((m) => m.id === data.id)) msgs = [...msgs, data];
				if (pending && data.text === pending) pending = '';
				break;
		}
	}

	async function open() {
		ctrl?.abort();
		const c = new AbortController();
		ctrl = c;
		phase = 'connecting';
		lines = [];
		info = null;
		pulse = null;
		failure = '';
		try {
			const headers: Record<string, string> = { Accept: 'text/event-stream' };
			const tok = getToken();
			if (tok) headers.Authorization = 'Bearer ' + tok;
			const r = await fetch('/v1/items/' + encodeURIComponent(id) + '/live?' + new URLSearchParams({ project }), { headers, signal: c.signal });
			if (!r.ok || !r.body) {
				const body = await r.json().catch(() => ({}));
				failure = body?.error || r.statusText;
				phase = 'error';
				return;
			}
			const reader = r.body.pipeThrough(new TextDecoderStream()).getReader();
			let buf = '';
			for (;;) {
				const { value, done } = await reader.read();
				if (done) break;
				buf += value;
				let cut: number;
				while ((cut = buf.indexOf('\n\n')) >= 0) {
					const block = buf.slice(0, cut);
					buf = buf.slice(cut + 2);
					let event = 'message';
					let data = '';
					for (const l of block.split('\n')) {
						if (l.startsWith('event: ')) event = l.slice(7);
						else if (l.startsWith('data: ')) data += l.slice(6);
					}
					if (!data) continue; // a ping
					try {
						handle(event, JSON.parse(data));
					} catch {
						/* not JSON */
					}
				}
			}
			if (ctrl === c && (phase as string) === 'live') phase = 'ended';
		} catch (e) {
			if (c.signal.aborted) return;
			failure = String(e);
			phase = 'error';
		}
	}

	// One stream while the tab is open; again when another session starts on the item.
	$effect(() => {
		void running;
		void project;
		void id;
		untrack(open);
		return () => ctrl?.abort();
	});
	onDestroy(() => ctrl?.abort());

	// The owner's own messages are on the right; the agents' and Barnraise's on the left.
	const mine = (m: Msg) => m.from === 'owner' || m.from.startsWith('owner@');

	// The session's lines and the item's messages as one conversation, by time.
	const entries = $derived.by(() => {
		type Timed = { at: number; e: ChatEntry };
		const out: Timed[] = [];
		const ms = (s?: string) => (s ? Date.parse(s) || 0 : 0);
		let lastTool: ChatEntry | null = null;
		lines.forEach((l, i) => {
			const key = 'l' + i;
			switch (l.kind) {
				case 'text':
					out.push({ at: ms(l.at), e: { key, role: 'assistant', text: l.text, at: l.at } });
					break;
				case 'thinking':
					out.push({ at: ms(l.at), e: { key, role: 'tool', tool: 'Thinking', input: l.text, result: l.text, at: l.at } });
					break;
				case 'tool':
					lastTool = { key, role: 'tool', tool: l.tool, input: l.text, at: l.at };
					out.push({ at: ms(l.at), e: lastTool });
					break;
				case 'result':
					if (lastTool && !lastTool.result) {
						lastTool.result = l.text || '(—)';
						lastTool.failed = l.error;
					}
					break;
				case 'end':
					out.push({ at: ms(l.at), e: { key, role: 'note', tone: 'muted', text: t('agentEnded') + (l.text ? ': ' + l.text : ''), at: l.at } });
					break;
				default:
					out.push({ at: ms(l.at), e: { key, role: 'note', tone: 'fail', text: l.text, at: l.at } });
			}
		});
		for (const m of msgs) {
			out.push({
				at: ms(m.at),
				e: mine(m)
					? { key: 'm' + m.id, role: 'user', text: m.text, at: m.at }
					: { key: 'm' + m.id, role: 'assistant', text: m.text, at: m.at, author: m.from, meta: '→ ' + m.to }
			});
		}
		return out.sort((a, b) => a.at - b.at).map((x) => x.e);
	});
	const lastLine = $derived(lines.length ? lines[lines.length - 1].at : '');
	const working = $derived(phase === 'live');

	async function send(v: string) {
		if (sending) return;
		sending = true;
		pending = v;
		text = '';
		try {
			await api.sendMessage({ project, message: { to: 'item:' + id, itemId: id, text: v } });
		} catch (err) {
			pending = '';
			text = v;
			failure = t('error', String(err));
		} finally {
			sending = false;
		}
	}
</script>

<section class="flex h-[calc(100dvh-16rem)] min-h-[28rem] flex-col overflow-hidden rounded-2xl border bg-card/30">
	<header class="space-y-2 border-b px-4 py-3">
		<div class="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
			<span class="grid size-7 place-items-center rounded-full bg-st-review/15 text-st-review ring-1 ring-st-review/20"><BotIcon class="size-3.5" /></span>
			{#if info}
				<span class="text-sm font-medium text-foreground">{info.agent}</span>
				<span>{info.role}</span><span>{t('step')}: {info.step}</span><span class="num">{info.model}</span><span>@ {info.machine}</span>
				{#if info.since}<span class="num ml-auto">{since(info.since)}</span>{/if}
			{:else}
				<span class="text-sm font-medium text-foreground">{t('agentAgent')}</span>
			{/if}
			<span class="num inline-flex items-center gap-1.5 {info ? '' : 'ml-auto'}" role="status">
				{#if phase === 'connecting'}<Spinner class="size-3" />{t('connecting')}
				{:else if phase === 'live'}<span class="size-2 rounded-full bg-st-work"></span>{t('live')}
				{:else if phase === 'ended'}{t('agentEnded')}
				{:else if phase === 'idle'}{t('agentIdle')}{/if}
			</span>
		</div>
		{#if phase === 'live'}<AgentPulse {pulse} {lastLine} />{/if}
		{#if phase === 'elsewhere'}<p class="text-sm" role="alert">{t('agentElsewhere', elsewhere.machine)}{#if elsewhere.why}<span class="block text-xs text-muted-foreground">{elsewhere.why}</span>{/if}</p>{/if}
		{#if phase === 'error' && failure}<p class="text-sm text-st-fail" role="alert">{t('error', failure)}</p>{/if}
	</header>

	<ChatThread {entries} busy={working} working={pulse?.text === 'waiting' ? t('agentWaiting') : ''} {pending} authorIcon={BotIcon} replies={false}>
		{#snippet empty()}
			<p class="py-8 text-center text-sm text-muted-foreground">{phase === 'idle' ? t('agentIdle') : t('agentNoMessages')}</p>
		{/snippet}
	</ChatThread>

	<ChatComposer bind:text onsend={send} {sending} placeholder={t('agentTalkPlaceholder')} hint={t('agentReadsNext')} focusKey={false} />
</section>
