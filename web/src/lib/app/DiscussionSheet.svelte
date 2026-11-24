<script lang="ts">
	import * as Sheet from '$lib/components/ui/sheet';
	import { Button } from '$lib/components/ui/button';
	import { Textarea } from '$lib/components/ui/textarea';
	import { Spinner } from '$lib/components/ui/spinner';
	import SendIcon from '@lucide/svelte/icons/send-horizontal';
	import SparklesIcon from '@lucide/svelte/icons/sparkles';
	import UserIcon from '@lucide/svelte/icons/user';
	import type { DiscussionMessage } from '$lib/gen/barnraise/v1/barnraise_pb';
	import { api, errorText } from '$lib/api';
	import { t } from '$lib/i18n';
	import { ago, compact } from '$lib/format';
	import { session } from '$lib/session.svelte';
	import { discussion } from './discussion.svelte';
	import Markdown from './Markdown.svelte';

	// The owner talks a decision over with the advisor. The daemon runs the
	// advisor (read-only, the strongest model) and keeps the conversation.
	let messages = $state<DiscussionMessage[]>([]);
	let text = $state('');
	let busy = $state(false);
	let error = $state('');
	let list = $state<HTMLElement | null>(null);

	async function load() {
		if (!discussion.open || !discussion.thread) return;
		try {
			const r = await api.getDiscussion({ project: discussion.project, thread: discussion.thread });
			if (!busy) messages = r.messages;
		} catch (e) {
			if (!session.failed(e)) error = errorText(e);
		}
	}
	$effect(() => {
		void session.tick;
		void discussion.thread;
		void discussion.open;
		load();
	});
	$effect(() => {
		void messages.length;
		void busy;
		queueMicrotask(() => list?.scrollTo({ top: list.scrollHeight, behavior: 'smooth' }));
	});

	async function send(e?: Event) {
		e?.preventDefault();
		const v = text.trim();
		if (!v || busy) return;
		busy = true;
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
	const SUGGEST = ['discussWhat', 'discussRisk', 'discussRecommend'];
</script>

<Sheet.Root bind:open={discussion.open}>
	<Sheet.Content side="right" class="flex w-full flex-col gap-0 p-0 sm:max-w-xl">
		<Sheet.Header class="border-b p-4">
			<Sheet.Title class="flex items-center gap-2"><SparklesIcon class="size-4 text-st-review" />{t('discussTitle')}</Sheet.Title>
			<Sheet.Description class="num line-clamp-2 text-xs">{discussion.thread} · {discussion.project} · {discussion.title}</Sheet.Description>
		</Sheet.Header>
		<div bind:this={list} class="min-h-0 flex-1 space-y-4 overflow-y-auto p-4">
			{#if !messages.length && !busy}
				<div class="space-y-3 rounded-lg border border-dashed p-4 text-sm text-muted-foreground">
					<p>{t('discussIntro')}</p>
					<div class="flex flex-wrap gap-2">
						{#each SUGGEST as k (k)}<button class="rounded-full border px-2.5 py-1 text-xs hover:bg-muted" onclick={() => (text = t(k))}>{t(k)}</button>{/each}
					</div>
				</div>
			{/if}
			{#each messages as m, i (i)}
				{#if m.role === 'owner'}
					<div class="flex justify-end gap-2">
						<div class="max-w-[85%] rounded-2xl rounded-tr-sm bg-primary px-3.5 py-2 text-sm whitespace-pre-wrap text-primary-foreground">{m.text}</div>
						<span class="mt-1 grid size-6 shrink-0 place-items-center rounded-full bg-muted"><UserIcon class="size-3.5" /></span>
					</div>
				{:else}
					<div class="flex gap-2">
						<span class="mt-1 grid size-6 shrink-0 place-items-center rounded-full bg-st-review/15 text-st-review"><SparklesIcon class="size-3.5" /></span>
						<div class="min-w-0 flex-1 space-y-1.5">
							<div class="rounded-2xl rounded-tl-sm border bg-card px-3.5 py-2.5"><Markdown text={m.text} /></div>
							<p class="num text-[10px] text-muted-foreground">{m.model}{m.tokens ? ' · ' + compact(m.tokens) + ' tokens' : ''} · {ago(m.at)}</p>
						</div>
					</div>
				{/if}
			{/each}
			{#if busy}
				<div class="flex items-center gap-2 text-sm text-muted-foreground"><Spinner class="size-4" />{t('discussThinking')}</div>
			{/if}
			{#if error}<p class="rounded-md bg-st-fail/10 p-3 text-sm text-st-fail" role="alert">{t('error', error)}</p>{/if}
		</div>
		<form class="space-y-2 border-t p-3" onsubmit={send}>
			<Textarea bind:value={text} rows={3} placeholder={t('discussPlaceholder')} onkeydown={keydown} disabled={busy} class="resize-none" />
			<div class="flex items-center justify-between gap-2">
				<p class="text-[11px] text-muted-foreground">{t('discussFooter')}</p>
				<Button type="submit" size="sm" disabled={busy || !text.trim()}><SendIcon />{t('send')}</Button>
			</div>
		</form>
	</Sheet.Content>
</Sheet.Root>
