<script lang="ts">
	import { Switch } from '$lib/components/ui/switch';
	import PlaneIcon from '@lucide/svelte/icons/plane';
	import { toast } from 'svelte-sonner';
	import { api, errorText } from '$lib/api';
	import { t } from '$lib/i18n';
	import { session } from '$lib/session.svelte';

	// The global switch of the autopilot: on, the advisor takes the owner's
	// decisions in every project and leaves only what it cannot resolve.
	const on = $derived(session.projects.length > 0 && session.projects.every((p) => session.autopilot.includes(p)));
	const some = $derived(!on && session.autopilot.length > 0);
	let busy = $state(false);
	async function toggle(next: boolean) {
		busy = true;
		try {
			const r = await api.setAutopilot({ all: true, on: next });
			const failed = r.projects.filter((p) => p.error);
			if (failed.length) toast.error(t('autopilotFailed', failed.map((p) => p.project + ': ' + p.error).join(' · ')));
			else toast.success(next ? t('autopilotOnToast') : t('autopilotOffToast'));
			await session.readProjects();
			session.bump(0);
		} catch (e) {
			if (!session.failed(e)) toast.error(t('error', errorText(e)));
		} finally {
			busy = false;
		}
	}
</script>

<label class="inline-flex h-8 cursor-pointer items-center gap-1.5 rounded-md border px-2 text-xs font-medium {on ? 'border-st-review/50 bg-st-review/10 text-st-review' : some ? 'border-st-review/30 text-st-review' : 'text-muted-foreground'}"
	title={on ? t('autopilotOnHelp') : some ? t('autopilotSome', session.autopilot.join(', ')) : t('autopilotOffHelp')}>
	<PlaneIcon class="size-3.5 shrink-0" /><span class="hidden whitespace-nowrap sm:inline">{t('autopilot')}</span>
	<Switch checked={on} disabled={busy} onCheckedChange={(v) => toggle(v)} aria-label={t('autopilot')} class="scale-90" />
</label>
