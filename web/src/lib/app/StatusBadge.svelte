<script lang="ts">
	import { SOFT, label, toneOf, type Tone } from '$lib/status';
	import { tm } from '$lib/i18n';
	import StatusIcon from './StatusIcon.svelte';

	let { status, tone, map = '', icon = true, class: cls = '' }: { status: string; tone?: Tone; map?: string; icon?: boolean; class?: string } = $props();
	const t = $derived(tone || toneOf(status));
	const text = $derived(map ? tm(map, status) : label(status));
</script>

<span class="inline-flex h-5 items-center gap-1 rounded-md px-1.5 text-xs font-medium whitespace-nowrap {SOFT[t]} {cls}">
	{#if icon}<StatusIcon tone={t} size={12} spin={t === 'work'} />{/if}{text}
</span>
