<script lang="ts">
	import { tm } from '$lib/i18n';
	let { health }: { health: string } = $props();
	const color: Record<string, string> = { green: 'bg-success', yellow: 'bg-warning', red: 'bg-destructive' };
</script>

<span class="inline-flex items-center gap-1.5" title={tm('health', health)}>
	<span class="size-2 rounded-full {color[health] || 'bg-muted-foreground/40'}" aria-hidden="true"></span>
	<span class="sr-only">{tm('health', health)}</span>
</span>
