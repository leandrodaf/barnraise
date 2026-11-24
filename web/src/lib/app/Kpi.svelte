<script lang="ts">
	import type { Component } from 'svelte';
	import ChevronRightIcon from '@lucide/svelte/icons/chevron-right';
	import { TEXT, TINT, type Tone } from '$lib/status';

	let { label, value, hint = '', tone = 'idle', icon: Icon, href = '', linkText = '' }:
		{ label: string; value: string | number; hint?: string; tone?: Tone; icon?: Component; href?: string; linkText?: string } = $props();
</script>

<div class="relative min-w-0 overflow-hidden rounded-xl border bg-gradient-to-br {TINT[tone]} to-transparent bg-card p-3 sm:p-4 transition-colors hover:border-foreground/15">
	<div class="flex items-start justify-between gap-2">
		<p class="min-w-0 text-sm text-muted-foreground">{label}</p>
		{#if Icon}<span class="grid size-7 place-items-center rounded-md border bg-background/60 {TEXT[tone]}"><Icon class="size-3.5" /></span>{/if}
	</div>
	<p class="num mt-2 truncate text-2xl font-semibold tracking-tight sm:text-3xl" title={String(value)}>{value}</p>
	{#if hint}<p class="mt-1 line-clamp-2 text-xs break-words text-muted-foreground">{hint}</p>{/if}
	{#if href}<a {href} class="mt-3 inline-flex items-center gap-0.5 text-xs font-medium hover:underline">{linkText}<ChevronRightIcon class="size-3" /></a>{/if}
</div>
