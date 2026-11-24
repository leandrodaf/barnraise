<script lang="ts">
	import type { Component, Snippet } from 'svelte';
	// Every screen opens the same way as an item: a quiet line of facts in mono,
	// the title (with its icon), an optional line of badges, then the actions.
	let { title, meta = [], subtitle = '', icon: Icon, badges, actions, children }:
		{ title: string; meta?: (string | number | false | null | undefined)[]; subtitle?: string; icon?: Component; badges?: Snippet; actions?: Snippet; children?: Snippet } = $props();
	$effect(() => {
		document.title = title + ' · Barnraise';
	});
	const facts = $derived(meta.filter((m) => m !== false && m !== null && m !== undefined && m !== ''));
</script>

<header class="mb-5 space-y-3">
	<div class="flex flex-col gap-3 lg:flex-row lg:items-start lg:justify-between">
		<div class="min-w-0 space-y-1.5">
			{#if facts.length}
				<p class="num flex flex-wrap items-center gap-x-2 text-xs text-muted-foreground">
					{#each facts as f, i (i)}{#if i}<span aria-hidden="true">·</span>{/if}<span>{f}</span>{/each}
				</p>
			{/if}
			<h1 class="flex items-center gap-2.5 text-xl font-semibold leading-tight tracking-tight">
				{#if Icon}<Icon class="size-5 shrink-0 text-muted-foreground" />{/if}<span class="min-w-0 break-words">{title}</span>
			</h1>
			{#if subtitle}<p class="text-sm text-muted-foreground">{subtitle}</p>{/if}
			{#if badges}<div class="flex flex-wrap items-center gap-1.5">{@render badges()}</div>{/if}
		</div>
		{#if actions}<div class="flex shrink-0 flex-wrap items-center gap-2">{@render actions()}</div>{/if}
	</div>
	{#if children}{@render children()}{/if}
</header>
