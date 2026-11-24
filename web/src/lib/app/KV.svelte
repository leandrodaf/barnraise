<script lang="ts">
	import type { Snippet } from 'svelte';
	// Rows with an empty value are left out.
	let { rows }: { rows: [string, string | number | Snippet | null | undefined][] } = $props();
	const shown = $derived(rows.filter(([, v]) => v !== undefined && v !== null && v !== '' && v !== 0));
</script>

<dl class="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1.5 text-sm">
	{#each shown as [k, v] (k)}
		<dt class="text-muted-foreground">{k}</dt>
		<dd class="min-w-0 break-words">{#if typeof v === 'function'}{@render v()}{:else}{v}{/if}</dd>
	{/each}
</dl>
