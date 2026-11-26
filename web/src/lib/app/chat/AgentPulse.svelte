<script lang="ts" module>
	export type Pulse = { text: string; tool?: string; since?: string; last?: string };
</script>

<script lang="ts">
	// Whether the agent is working or stuck: what it does now (thinking,
	// writing, running a tool) and since when, and its last sign of life.
	// A tool may run long (tests, builds); silence while it thinks may not.
	import { t } from '$lib/i18n';
	import { toolLook, shortPath, clock } from './chat';

	let { pulse, lastLine = '' }: { pulse: Pulse | null; lastLine?: string } = $props();

	let now = $state(Date.now());
	$effect(() => {
		const iv = setInterval(() => (now = Date.now()), 1000);
		return () => clearInterval(iv);
	});
	const secs = (iso?: string) => {
		const ms = iso ? Date.parse(iso) : NaN;
		return Number.isFinite(ms) && ms > 0 ? Math.max(0, (now - ms) / 1000) : NaN;
	};

	// The state word comes first in the pulse's text ("tool: command: make test").
	const mode = $derived(pulse ? pulse.text.split(':')[0] : '');
	const input = $derived(pulse && pulse.text.includes(':') ? pulse.text.slice(pulse.text.indexOf(':') + 1).trim() : '');
	const inState = $derived(secs(pulse?.since));
	// Silence: since the last sign of life (the pulse's, else the last line's).
	const silent = $derived.by(() => {
		const a = secs(pulse?.last);
		const b = secs(lastLine);
		if (Number.isFinite(a) && Number.isFinite(b)) return Math.min(a, b);
		return Number.isFinite(a) ? a : b;
	});

	const STALL = 180;
	const DEAD = 600;
	const level = $derived.by(() => {
		if (mode === 'tool') return inState > DEAD * 3 ? 'warn' : 'ok';
		if (!Number.isFinite(silent)) return 'ok';
		if (silent > DEAD) return 'dead';
		if (silent > STALL) return 'warn';
		return 'ok';
	});
	const label = $derived.by(() => {
		switch (mode) {
			case 'tool':
				return t('pulseTool', toolLook(pulse?.tool).label);
			case 'writing':
				return t('pulseWriting');
			case 'thinking':
				return t('pulseThinking');
			case 'waiting':
				return t('pulseWaiting');
			default:
				return t('pulseWorking');
		}
	});
	const dot = { ok: 'bg-st-done', warn: 'bg-st-needs', dead: 'bg-st-fail' };
	const box = { ok: 'border-st-done/25 bg-st-done/[0.06]', warn: 'border-st-needs/40 bg-st-needs/10', dead: 'border-st-fail/40 bg-st-fail/10' };
</script>

<div class="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-xl border px-3 py-2 text-xs {box[level]}" role="status" aria-live="polite">
	<span class="relative flex size-2.5 shrink-0">
		{#if level === 'ok'}<span class="absolute inline-flex size-full animate-ping rounded-full {dot[level]} opacity-60"></span>{/if}
		<span class="relative inline-flex size-2.5 rounded-full {dot[level]}"></span>
	</span>
	<span class="font-medium">{label}</span>
	{#if input}<code class="num min-w-0 max-w-[24rem] truncate text-muted-foreground" title={input}>{shortPath(input)}</code>{/if}
	{#if Number.isFinite(inState)}<span class="num text-muted-foreground">{t('pulseFor', clock(inState))}</span>{/if}
	<span class="num ml-auto text-muted-foreground">
		{#if Number.isFinite(silent)}{t('pulseLast', clock(silent))}{/if}
	</span>
	{#if level === 'warn' && mode === 'tool'}
		<p class="basis-full text-st-needs">{t('pulseLongTool')}</p>
	{:else if level === 'warn'}
		<p class="basis-full text-st-needs">{t('pulseQuiet', clock(silent))}</p>
	{:else if level === 'dead'}
		<p class="basis-full text-st-fail">{t('pulseStuck', clock(silent))}</p>
	{/if}
</div>
