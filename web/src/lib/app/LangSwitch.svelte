<script lang="ts">
	import LanguagesIcon from '@lucide/svelte/icons/languages';
	import { locale, setLang, type Lang } from '$lib/locale.svelte';
	import { t } from '$lib/i18n';
	// English or Brazilian Portuguese, switched in place.
	let { compact = false }: { compact?: boolean } = $props();
	const LANGS: [Lang, string, string][] = [['en', 'EN', 'English'], ['pt-BR', 'PT', 'Português (BR)']];
</script>

<div class="flex items-center gap-1.5 {compact ? '' : 'px-2'}" role="group" aria-label={t('language')}>
	{#if !compact}<LanguagesIcon class="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />{/if}
	<div class="flex rounded-md border p-0.5">
		{#each LANGS as [code, short, name] (code)}
			<button
				class="num rounded px-2 py-0.5 text-[11px] font-medium transition-colors {locale.lang === code ? 'bg-foreground text-background' : 'text-muted-foreground hover:text-foreground'}"
				aria-pressed={locale.lang === code}
				title={name}
				onclick={() => setLang(code)}>{short}</button
			>
		{/each}
	</div>
</div>
