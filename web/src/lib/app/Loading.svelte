<script lang="ts">
	import { Skeleton } from '$lib/components/ui/skeleton';
	import { Button } from '$lib/components/ui/button';
	import * as Alert from '$lib/components/ui/alert';
	import { t } from '$lib/i18n';
	import type { Snippet } from 'svelte';

	// The skeleton the first time, the error with a retry, or the content.
	let { state, children }: { state: { data: unknown; error: string; loading: boolean; reload: () => void }; children: Snippet } = $props();
</script>

{#if state.data !== undefined}
	{#if state.error}
		<Alert.Root variant="destructive" class="mb-4"><Alert.Description>{t('error', state.error)}</Alert.Description></Alert.Root>
	{/if}
	{@render children()}
{:else if state.error}
	<Alert.Root variant="destructive">
		<Alert.Description class="flex items-center justify-between gap-4">
			<span>{t('error', state.error)}</span>
			<Button size="sm" variant="outline" onclick={() => state.reload()}>{t('retry')}</Button>
		</Alert.Description>
	</Alert.Root>
{:else}
	<div class="space-y-4" aria-busy="true" aria-label={t('loading')}>
		<Skeleton class="h-8 w-48" />
		<div class="grid gap-4 md:grid-cols-3"><Skeleton class="h-32" /><Skeleton class="h-32" /><Skeleton class="h-32" /></div>
		<Skeleton class="h-64" />
	</div>
{/if}
