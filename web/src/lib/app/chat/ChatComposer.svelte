<script lang="ts">
	// The message box of every conversation with an AI: it grows with the
	// text, Enter sends and Shift+Enter breaks the line, Esc stops the answer
	// under way, "/" anywhere puts the cursor in it, and it dictates where
	// the browser can.
	import { onMount, onDestroy, type Snippet } from 'svelte';
	import { localDictationAvailable, recordDictation } from './dictation';
	import { t } from '$lib/i18n';
	import { Button } from '$lib/components/ui/button';
	import { Spinner } from '$lib/components/ui/spinner';
	import SendIcon from '@lucide/svelte/icons/arrow-up';
	import SquareIcon from '@lucide/svelte/icons/square';
	import MicIcon from '@lucide/svelte/icons/mic';
	import MicOffIcon from '@lucide/svelte/icons/mic-off';

	let {
		text = $bindable(''),
		onsend,
		onstop,
		busy = false,
		sending = false,
		disabled = false,
		placeholder,
		hint = '',
		focusKey = true,
		chip,
		actions,
		footer
	}: {
		text?: string;
		onsend: (text: string) => void;
		// onstop: shown while busy, to stop the answer under way.
		onstop?: () => void;
		busy?: boolean;
		sending?: boolean;
		disabled?: boolean;
		placeholder: string;
		hint?: string;
		// focusKey: "/" anywhere focuses this box (one per page).
		focusKey?: boolean;
		chip?: Snippet;
		actions?: Snippet;
		footer?: Snippet;
	} = $props();

	let box: HTMLTextAreaElement | undefined = $state();
	export const focus = () => box?.focus();

	function send() {
		const v = text.trim();
		if (!v || sending || disabled || processing || listening) return;
		rec?.stop();
		onsend(v);
	}

	function key(e: KeyboardEvent) {
		if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
			e.preventDefault();
			send();
		} else if (e.key === 'Escape' && busy && onstop) {
			e.preventDefault();
			onstop();
		}
	}

	function onWindowKey(e: KeyboardEvent) {
		if (!focusKey) return;
		const el = e.target as HTMLElement;
		if (e.key === '/' && !e.metaKey && !e.ctrlKey && !/INPUT|TEXTAREA|SELECT/.test(el.tagName) && !el.isContentEditable) {
			e.preventDefault();
			box?.focus();
		}
	}

	// Dictation, where the browser has speech recognition.
	type Recognition = { lang: string; interimResults: boolean; continuous: boolean; start(): void; stop(): void; abort(): void; onerror: ((e: { error: string }) => void) | null; onresult: ((e: any) => void) | null; onend: (() => void) | null };
	const SR = typeof window !== 'undefined' ? ((window as any).SpeechRecognition || (window as any).webkitSpeechRecognition) : undefined;
	let rec: Recognition | null = null;
	let listening = $state(false);
	let local = $state(false);
	let processing = $state(false);
	let feedback = $state('');
	let stopLocal: (() => void) | undefined;
	const lifetime = new AbortController();
	onMount(() => { localDictationAvailable(lifetime.signal).then(v => local = v).catch(() => {}); });
	onDestroy(() => { lifetime.abort(); rec?.abort(); });
	function report(code: string) {
		feedback = t(['not-allowed','service-not-allowed','NotAllowedError'].includes(code) ? 'dictPermission'
			: ['audio-capture','NotFoundError','NotReadableError'].includes(code) ? 'dictMic'
			: code === 'no-speech' ? 'dictSilent' : 'dictFailed');
	}
	async function dictate() {
		if (listening) { if (stopLocal) stopLocal(); else rec?.stop(); return; }
		if (processing) return;
		feedback = '';
		if (local) {
			processing = true;
			feedback = t('dictWaiting');
			try {
				const words = await recordDictation(lifetime.signal, stop => {
					stopLocal = stop; listening = true; processing = false; feedback = t('dictRecording');
				}, () => { listening = false; processing = true; feedback = t('dictTranscribing'); });
				if (words) { text = (text ? text.trimEnd() + ' ' : '') + words; feedback = ''; box?.focus(); }
				else report('no-speech');
			} catch (e) { if (!lifetime.signal.aborted) report((e as Error).name); }
			finally { stopLocal = undefined; processing = false; listening = false; }
			return;
		}
		if (!SR) { report('unavailable'); return; }
		const base = text ? text.trimEnd() + ' ' : '';
		rec = new SR() as Recognition;
		rec.lang = document.documentElement.lang || navigator.language || 'pt-BR';
		rec.interimResults = true;
		rec.continuous = true;
		rec.onresult = (e: any) => {
			let said = '';
			for (let i = 0; i < e.results.length; i++) said += e.results[i][0].transcript;
			text = base + said;
		};
		rec.onerror = e => { if (!lifetime.signal.aborted) report(e.error); };
		rec.onend = () => { listening = false; if (feedback === t('dictRecording')) feedback = ''; };
		listening = true;
		try { rec.start(); feedback = t('dictRecording'); } catch (e) { listening = false; report((e as Error).name); }
	}
</script>

<svelte:window onkeydown={onWindowKey} />

<form class="px-3 pb-3 sm:px-6" onsubmit={(e) => { e.preventDefault(); send(); }}>
	<div class="mx-auto max-w-3xl rounded-2xl border bg-background shadow-sm transition focus-within:border-st-review/40 focus-within:shadow-md focus-within:ring-2 focus-within:ring-st-review/10">
		<textarea bind:this={box} bind:value={text} onkeydown={key} rows="1" {placeholder} aria-label={placeholder} {disabled}
			class="block max-h-56 min-h-12 w-full resize-none bg-transparent px-4 pt-3.5 pb-1 text-sm outline-none [field-sizing:content] placeholder:text-muted-foreground disabled:opacity-60"></textarea>
		<div class="flex items-center gap-2 px-2 pb-2">
			{@render chip?.()}
			<span class="hidden truncate pl-1 text-[11px] text-muted-foreground lg:block">{hint || t('convKeys')}</span>
			<span class="ml-auto"></span>
			{@render actions?.()}
			{#if SR || local}
				<Button type="button" size="icon" variant={listening ? 'default' : 'ghost'} class="size-8 rounded-full {listening ? 'animate-pulse bg-st-fail text-white hover:bg-st-fail' : ''}" onclick={dictate} disabled={processing || (disabled && !listening)} title={listening ? t('convDictateStop') : t('convDictate')} aria-label={listening ? t('convDictateStop') : t('convDictate')} aria-pressed={listening}>
					{#if listening}<MicOffIcon class="size-4" />{:else}<MicIcon class="size-4" />{/if}
				</Button>
			{/if}
			{#if busy && onstop}
				<Button type="button" size="icon" variant="outline" class="size-8 rounded-full" onclick={onstop} title={t('convStop')} aria-label={t('convStop')}><SquareIcon class="size-3 fill-current" /></Button>
			{/if}
			<Button type="submit" size="icon" class="size-8 rounded-full" disabled={!text.trim() || sending || disabled || listening || processing} title={t('convSend')} aria-label={t('convSend')}>
				{#if sending}<Spinner class="size-4" />{:else}<SendIcon class="size-4" />{/if}
			</Button>
		</div>
	</div>
	{#if feedback}<p class="mx-auto mt-1 max-w-3xl text-xs text-muted-foreground" role="status">{feedback}</p>{/if}
	{@render footer?.()}
</form>
