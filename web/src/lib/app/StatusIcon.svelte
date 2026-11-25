<script lang="ts">
	import type { Tone } from '$lib/status';
	import { TEXT } from '$lib/status';
	// The shape says the state without color: ring idle, half working,
	// diamond needs you, check done, cross failed, slashed blocked, stack queue.
	// Working turns a few times and rests: an endless turn costs CPU for as
	// long as the page is open (see --animate-ping in app.css).
	let { tone, size = 14, spin = false, class: cls = '' }: { tone: Tone; size?: number; spin?: boolean; class?: string } = $props();
</script>

<svg width={size} height={size} viewBox="0 0 16 16" class="shrink-0 {TEXT[tone]} {cls}" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.6">
	{#if tone === 'work' || tone === 'review'}
		<circle cx="8" cy="8" r="6" />
		<path d="M8 2a6 6 0 0 1 0 12z" fill="currentColor" stroke="none" style={spin ? 'transform-origin:8px 8px;animation:spin-slow 2.4s linear 3' : ''} />
	{:else if tone === 'needs'}
		<path d="M8 1.8 14.2 8 8 14.2 1.8 8z" fill="currentColor" fill-opacity=".2" stroke-linejoin="round" />
		<path d="M8 5v3.4M8 10.6v.2" stroke-linecap="round" />
	{:else if tone === 'done'}
		<circle cx="8" cy="8" r="6.2" fill="currentColor" stroke="none" />
		<path d="m5.2 8.2 1.9 1.9 3.8-4" stroke="var(--background)" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
	{:else if tone === 'fail'}
		<circle cx="8" cy="8" r="6" />
		<path d="m5.8 5.8 4.4 4.4m0-4.4-4.4 4.4" stroke-linecap="round" />
	{:else if tone === 'queue'}
		<rect x="2.5" y="4.5" width="8" height="8" rx="1.5" />
		<path d="M5.5 2.5h6.5a1.5 1.5 0 0 1 1.5 1.5v6.5" />
	{:else}
		<circle cx="8" cy="8" r="6" stroke-dasharray="2.2 1.6" />
	{/if}
</svg>
