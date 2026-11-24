<script lang="ts">
	import { BG, type Tone } from '$lib/status';
	let { tone, pulse = false }: { tone: Tone; pulse?: boolean } = $props();
</script>

<span class="relative inline-flex size-2 shrink-0" aria-hidden="true">
	{#if pulse}<span class="absolute inset-0 animate-ping rounded-full opacity-60 {BG[tone]}"></span>{/if}
	<span class="relative size-2 rounded-full {BG[tone]}"></span>
</span>
