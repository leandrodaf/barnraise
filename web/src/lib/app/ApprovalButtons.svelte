<script lang="ts">
	import { Button } from '$lib/components/ui/button';
	import { api } from '$lib/api';
	import { t } from '$lib/i18n';
	import { act } from './act';
	import { dialog } from './dialog.svelte';

	// What is approved (plan or delivery) comes from the daemon (ItemOverview.awaits).
	let { id, awaits, project }: { id: string; awaits: string; project: string } = $props();
	let busy = $state(false);
	async function approve() {
		busy = true;
		await act(() => api.approve({ project, target: id, what: awaits, approve: true }), t('approved'));
		busy = false;
	}
	function sendBack() {
		dialog.open({
			title: t('sendBackTitle', id),
			fields: [{ name: 'note', label: t('reason'), type: 'textarea', required: true }],
			submit: t('sendBack'),
			onSubmit: async (v) => {
				await api.approve({ project, target: id, what: awaits, approve: false, note: v.note });
				return t('sentBack');
			}
		});
	}
</script>

{#if awaits}
	<div class="flex flex-wrap gap-2">
		<Button size="sm" onclick={approve} disabled={busy}>{awaits === 'plan' ? t('approvePlan') : t('approveDelivery')}</Button>
		<Button size="sm" variant="outline" onclick={sendBack}>{t('sendBack')}</Button>
	</div>
{/if}
