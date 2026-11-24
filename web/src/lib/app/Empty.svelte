<script lang="ts">
	import { t } from '$lib/i18n';
	let { text = '' }: { text?: string } = $props();
</script>

<p class="py-2 text-sm text-muted-foreground">{text || t('none')}</p>
