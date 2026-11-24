<script lang="ts">
	import type { Snippet } from 'svelte';
	// The item's layout for every screen: the work on the left, a narrow panel
	// of facts on the right; the panel goes under the work on smaller screens.
	let { children, aside }: { children: Snippet; aside?: Snippet } = $props();
</script>

{#if aside}
	<div class="grid gap-4 xl:grid-cols-[minmax(0,1fr)_21rem] min-[1800px]:grid-cols-[minmax(0,1fr)_25rem] min-[2400px]:grid-cols-[minmax(0,1fr)_28rem]">
		<div class="min-w-0 space-y-4">{@render children()}</div>
		<aside class="min-w-0 space-y-4">{@render aside()}</aside>
	</div>
{:else}
	<div class="min-w-0 space-y-4">{@render children()}</div>
{/if}
