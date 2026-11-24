<script lang="ts">
	import type { Snippet } from 'svelte';
	import { toneOfItem, signalText } from '$lib/status';
	import { session } from '$lib/session.svelte';
	import StatusIcon from './StatusIcon.svelte';
	import { openItem } from './open';
	import type { Row } from './items';

	// One line of a list: state, title, quiet facts in mono, then actions under it.
	let { r, meta, below }: { r: Row; meta?: Snippet; below?: Snippet } = $props();
	const tone = $derived(toneOfItem(r));
</script>

<div class="group flex items-start gap-3 px-4 py-3 transition-colors hover:bg-muted/40">
	<StatusIcon {tone} spin={r.activity === 'working'} class="mt-0.5" />
	<div class="min-w-0 flex-1 space-y-2">
		<button class="block w-full min-w-0 text-left" onclick={() => openItem(r.project, r.id)}>
			<p class="line-clamp-2 text-sm font-medium group-hover:underline">{r.title}</p>
			<p class="num mt-0.5 flex flex-wrap gap-x-3 text-[11px] text-muted-foreground">
				<span>{r.id}</span>{#if !session.scope}<span>{r.project}</span>{/if}<span>{signalText(r)}</span>{#if meta}{@render meta()}{/if}
			</p>
		</button>
		{#if below}{@render below()}{/if}
	</div>
</div>
