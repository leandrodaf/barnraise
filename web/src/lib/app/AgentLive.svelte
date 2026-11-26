<script lang="ts">
	// The Agent tab of an item: what the agent running on it writes and the
	// tools it calls, live, and a conversation with it. The stream opens with
	// the tab and closes with it (ADR-0048): nobody watching costs nothing.
	import { onDestroy, untrack } from 'svelte';
	import { Button } from '$lib/components/ui/button';
	import { Spinner } from '$lib/components/ui/spinner';
	import { Textarea } from '$lib/components/ui/textarea';
	import SendIcon from '@lucide/svelte/icons/send';
	import WrenchIcon from '@lucide/svelte/icons/wrench';
	import CornerDownRightIcon from '@lucide/svelte/icons/corner-down-right';
	import { api, getToken } from '$lib/api';
	import { t } from '$lib/i18n';
	import { since } from '$lib/format';
	import Section from '$lib/app/Section.svelte';

	// running: who runs on the item now (agent names), so a new session reopens the stream.
	let { project, id, running = '' }: { project: string; id: string; running?: string } = $props();

	type Line = { at: string; kind: string; tool?: string; text?: string; error?: boolean };
	type Info = { agent: string; role: string; step: string; provider: string; model: string; machine: string; since?: string };
	type Msg = { id: string; from: string; to: string; text: string; at: string };

	let phase = $state<'connecting' | 'live' | 'idle' | 'elsewhere' | 'ended' | 'error'>('connecting');
	let info = $state<Info | null>(null);
	let lines = $state<Line[]>([]);
	let msgs = $state<Msg[]>([]);
	let elsewhere = $state({ machine: '', why: '' });
	let failure = $state('');
	let text = $state('');
	let sending = $state(false);
	let sent = $state('');
	let box = $state<HTMLElement | null>(null);
	let follow = true;
	let ctrl: AbortController | null = null;
	const MAX_LINES = 400;

	function handle(event: string, data: any) {
		switch (event) {
			case 'session':
				info = data;
				phase = 'live';
				break;
			case 'line':
				lines = lines.length >= MAX_LINES ? [...lines.slice(-MAX_LINES + 1), data] : [...lines, data];
				break;
			case 'end':
				phase = 'ended';
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

	// Keep the newest line in view unless the owner scrolled up to read.
	$effect(() => {
		void lines.length;
		if (box && follow) queueMicrotask(() => box && (box.scrollTop = box.scrollHeight));
	});
	const onScroll = () => {
		if (box) follow = box.scrollHeight - box.scrollTop - box.clientHeight < 40;
	};

	async function send(e?: Event) {
		e?.preventDefault();
		const v = text.trim();
		if (!v || sending) return;
		sending = true;
		sent = '';
		try {
			await api.sendMessage({ project, message: { to: 'item:' + id, itemId: id, text: v } });
			text = '';
			sent = t('agentSent');
		} catch (err) {
			sent = t('error', String(err));
		} finally {
			sending = false;
		}
	}
	const keydown = (e: KeyboardEvent) => {
		if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) send();
	};
	// The owner's own messages are on the right; the agents' and Barnraise's on the left.
	const mine = (m: Msg) => m.from === 'owner' || m.from.startsWith('owner@');
</script>

<div class="space-y-4">
	<Section title={t('agentLive')} sub={t('agentLiveHelp')} pad={false}>
		{#snippet right()}
			<span class="num inline-flex items-center gap-1.5 text-xs text-muted-foreground" role="status">
				{#if phase === 'connecting'}<Spinner class="size-3" />{t('connecting')}
				{:else if phase === 'live'}<span class="size-2 rounded-full bg-st-work"></span>{t('live')}
				{:else if phase === 'ended'}{t('agentEnded')}{/if}
			</span>
		{/snippet}
		{#if info}
			<p class="num flex flex-wrap gap-x-3 gap-y-1 border-b px-4 py-2 text-xs text-muted-foreground">
				<span class="font-medium text-foreground">{info.agent}</span>
				<span>{info.role}</span><span>{t('step')}: {info.step}</span><span>{info.model}</span><span>@ {info.machine}</span>
				{#if info.since}<span class="ml-auto">{since(info.since)}</span>{/if}
			</p>
		{/if}
		{#if phase === 'idle'}<p class="p-4 text-sm text-muted-foreground">{t('agentIdle')}</p>
		{:else if phase === 'elsewhere'}<p class="p-4 text-sm" role="alert">{t('agentElsewhere', elsewhere.machine)}{#if elsewhere.why}<span class="block text-xs text-muted-foreground">{elsewhere.why}</span>{/if}</p>
		{:else if phase === 'error'}<p class="p-4 text-sm text-st-fail" role="alert">{t('error', failure)}</p>
		{:else}
			<div bind:this={box} onscroll={onScroll} class="num max-h-[28rem] min-h-24 overflow-y-auto px-4 py-2 font-mono text-[11px] leading-relaxed" aria-live="polite">
				{#each lines as l, i (i)}
					{#if l.kind === 'text'}<p class="my-1.5 whitespace-pre-wrap font-sans text-sm [overflow-wrap:anywhere]">{l.text}</p>
					{:else if l.kind === 'tool'}<p class="flex gap-1.5 text-st-review [overflow-wrap:anywhere]"><WrenchIcon class="mt-0.5 size-3 shrink-0" /><span><span class="font-semibold">{l.tool}</span> {l.text}</span></p>
					{:else if l.kind === 'result'}<p class="flex gap-1.5 whitespace-pre-wrap text-muted-foreground [overflow-wrap:anywhere] {l.error ? 'text-st-fail' : ''}"><CornerDownRightIcon class="mt-0.5 size-3 shrink-0" />{l.text}</p>
					{:else if l.kind === 'end'}<p class="my-1 text-muted-foreground">— {t('agentEnded')} —</p>
					{:else}<p class="text-st-fail [overflow-wrap:anywhere]">{l.text}</p>{/if}
				{:else}
					{#if phase === 'live'}<p class="py-2 font-sans text-xs text-muted-foreground">{t('agentWaiting')}</p>{/if}
				{/each}
			</div>
		{/if}
	</Section>

	<Section title={t('agentTalk')} sub={t('agentTalkHelp')}>
		<div class="space-y-2">
			{#each msgs as m (m.id)}
				{@const own = mine(m)}
				<div class="flex {own ? 'justify-end' : 'justify-start'}">
					<div class="max-w-[85%] rounded-2xl px-3.5 py-2 text-sm whitespace-pre-wrap [overflow-wrap:anywhere] {own ? 'rounded-br-md bg-muted' : 'rounded-bl-md border bg-card'}">
						<p class="num mb-0.5 text-[10px] text-muted-foreground">{m.from} → {m.to} · {since(m.at)}</p>
						{m.text}
					</div>
				</div>
			{:else}<p class="text-xs text-muted-foreground">{t('agentNoMessages')}</p>{/each}
		</div>
		<form class="mt-3 space-y-2" onsubmit={send}>
			<Textarea bind:value={text} rows={3} placeholder={t('agentPlaceholder')} onkeydown={keydown} disabled={sending} class="resize-none" aria-label={t('msgText')} />
			<div class="flex items-center justify-between gap-2">
				<p class="text-[11px] text-muted-foreground">{sent || t('agentReadsNext')}</p>
				<Button type="submit" size="sm" disabled={sending || !text.trim()}>{#if sending}<Spinner class="size-3.5" />{:else}<SendIcon />{/if}{t('send')}</Button>
			</div>
		</form>
	</Section>
</div>
