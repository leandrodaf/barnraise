<script lang="ts">
	// A small area chart of 0..100 values, newest on the right.
	let { values, tone = 'work', height = 28, label = '' }: { values: number[]; tone?: string; height?: number; label?: string } = $props();
	const W = 120;
	const pts = $derived.by(() => {
		const v = values.slice(-60);
		if (v.length < 2) return '';
		return v.map((y, i) => `${((i / (v.length - 1)) * W).toFixed(1)},${(height - (Math.max(0, Math.min(100, y)) / 100) * (height - 2) - 1).toFixed(1)}`).join(' ');
	});
	const color = $derived(`var(--st-${tone})`);
</script>

{#if pts}
	<svg viewBox="0 0 {W} {height}" preserveAspectRatio="none" class="h-full w-full" role="img" aria-label={label}>
		<polygon points="0,{height} {pts} {W},{height}" fill={color} fill-opacity="0.14" />
		<polyline points={pts} fill="none" stroke={color} stroke-width="1.4" vector-effect="non-scaling-stroke" stroke-linejoin="round" />
	</svg>
{:else}
	<div class="h-full w-full rounded bg-muted/40"></div>
{/if}
