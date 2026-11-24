<script lang="ts">
	import * as Dialog from '$lib/components/ui/dialog';
	import { Kbd } from '$lib/components/ui/kbd';
	import { t } from '$lib/i18n';
	import { keys, GO_KEYS } from './keys.svelte';
	import { NAV } from './nav';
</script>

<Dialog.Root bind:open={keys.help}>
	<Dialog.Content class="sm:max-w-md">
		<Dialog.Header><Dialog.Title>{t('shortcuts')}</Dialog.Title></Dialog.Header>
		<ul class="space-y-2 text-sm">
			<li class="flex justify-between"><span>{t('palette')}</span><span><Kbd>Ctrl K</Kbd> <Kbd>/</Kbd></span></li>
			{#each GO_KEYS as [k, r] (k)}<li class="flex justify-between"><span>{t(NAV.find((n) => n.name === r)?.label || r)}</span><span><Kbd>g</Kbd> <Kbd>{k}</Kbd></span></li>{/each}
			<li class="flex justify-between"><span>{t('shortcuts')}</span><Kbd>?</Kbd></li>
		</ul>
	</Dialog.Content>
</Dialog.Root>
