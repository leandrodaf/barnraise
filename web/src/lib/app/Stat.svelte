<script lang="ts">
	let { label, value, alert = false }: { label: string; value: string | number; alert?: boolean } = $props();
</script>

<div class="rounded-xl border bg-card p-4 {alert ? 'border-st-fail/40 bg-st-fail/5' : ''}">
	<p class="num text-xl font-semibold tracking-tight {alert ? 'text-st-fail' : ''}">{value}</p>
	<p class="mt-1 text-xs text-muted-foreground">{label}</p>
</div>
