<script lang="ts">
	import * as Select from '$lib/components/ui/select';
	import LayersIcon from '@lucide/svelte/icons/layers';
	import FolderIcon from '@lucide/svelte/icons/folder';
	import PlusIcon from '@lucide/svelte/icons/plus';
	import { addProjectDialog } from '$lib/app/addproject.svelte.ts';
	import { t } from '$lib/i18n';
	import { session } from '$lib/session.svelte';

	const ALL = '__all__';
	const ADD = '__add__';
	function pick(v: string) {
		if (v === ADD) {
			addProjectDialog.open = true;
			return;
		}
		session.setScope(v === ALL ? '' : v);
	}
</script>

<Select.Root type="single" value={session.scope || ALL} onValueChange={pick}>
	<Select.Trigger size="sm" class="h-8 max-w-[11rem] gap-1.5 border-none bg-transparent px-2 font-medium shadow-none hover:bg-muted sm:max-w-none" aria-label={t('scope')}>
		{#if session.scope}<FolderIcon class="size-3.5 text-muted-foreground" /><span class="truncate">{session.scope}</span>
		{:else}<LayersIcon class="size-3.5 text-muted-foreground" /><span class="truncate">{t('allProjects')}</span>{/if}
	</Select.Trigger>
	<Select.Content>
		<Select.Item value={ALL} label={t('allProjects')}><LayersIcon class="size-3.5" />{t('allProjects')}</Select.Item>
		<Select.Separator />
		{#each session.projects as p (p)}<Select.Item value={p} label={p}><FolderIcon class="size-3.5" />{p}</Select.Item>{/each}
		<Select.Separator />
		<Select.Item value={ADD} label={t('apAddTitle')}><PlusIcon class="size-3.5" />{t('apAddTitle')}</Select.Item>
	</Select.Content>
</Select.Root>
