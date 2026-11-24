<script lang="ts">
	import { Button } from '$lib/components/ui/button';
	import StarIcon from '@lucide/svelte/icons/star';
	import type { Question } from '$lib/gen/barnraise/v1/barnraise_pb';
	import { api } from '$lib/api';
	import { t } from '$lib/i18n';
	import { ago, when } from '$lib/format';
	import { session } from '$lib/session.svelte';
	import { act } from './act';
	import { openItem } from './open';
	import StatusIcon from './StatusIcon.svelte';
	import MessagesIcon from '@lucide/svelte/icons/messages-square';
	import WandIcon from '@lucide/svelte/icons/wand-sparkles';
	import { discussion } from './discussion.svelte';

	let { q, project = '' }: { q: Question; project?: string } = $props();
	const proj = $derived(project || session.project);
	let busy = $state(false);
	let more = $state(false);
	const long = $derived((q.context || '').length + (q.decision || '').length > 320);
	async function answer(label: string) {
		busy = true;
		await act(() => api.answer({ project: proj, id: q.id, answer: label }), t('answered', label));
		busy = false;
	}
</script>

<article class="min-w-0 rounded-lg border border-l-2 {q.urgent ? 'border-l-st-fail bg-st-fail/5' : 'border-l-st-needs bg-st-needs/[0.04]'} p-4">
	<div class="flex min-w-0 items-start gap-3">
		<StatusIcon tone={q.urgent ? 'fail' : 'needs'} class="mt-0.5" />
		<div class="min-w-0 flex-1 space-y-2">
			<p class="font-medium leading-snug [overflow-wrap:anywhere] {long && !more ? 'line-clamp-5' : ''}">{q.decision}</p>
			{#if q.context}<p class="text-sm text-muted-foreground [overflow-wrap:anywhere] {long && !more ? 'line-clamp-4' : ''}">{q.context}</p>{/if}
			{#if long}<button class="text-xs font-medium hover:underline" onclick={() => (more = !more)}>{more ? t('showLess') : t('showMore')}</button>{/if}
			<p class="num flex flex-wrap gap-x-3 text-[11px] text-muted-foreground">
				{#if q.urgent}<span class="font-semibold text-st-fail">{t('urgent')}</span>{/if}
				{#if q.itemId}<button class="hover:text-foreground hover:underline" onclick={() => openItem(proj, q.itemId)}>{q.itemId}</button>{/if}
				<span>{proj}</span>
				{#if q.askedBy}<span>{t('askedBy', q.askedBy, ago(q.askedAt))}</span>{/if}
				{#if q.deadline}<span>{t('deadline', when(q.deadline))}</span>{/if}
			</p>
			<div class="flex flex-wrap gap-2 pt-1">
				<Button size="sm" variant="secondary" class="h-auto min-h-8 py-1.5" onclick={() => discussion.show(proj, q.id, q.decision)}><MessagesIcon />{t('discuss')}</Button>
				<Button size="sm" variant="secondary" class="h-auto min-h-8 py-1.5" disabled={busy} title={t('decideForMeHelp')} onclick={() => discussion.show(proj, q.id, q.decision, true)}><WandIcon />{t('decideForMe')}</Button>
				{#each q.options as o (o.label)}
					<Button size="sm" class="h-auto min-h-8 max-w-full py-1.5" variant={o.recommended ? 'default' : 'outline'} disabled={busy} onclick={() => answer(o.label)}>
						<span class="whitespace-normal text-left">{o.label}</span>{#if o.recommended}<StarIcon class="size-3" aria-hidden="true" /><span class="sr-only">({t('recommended')})</span>{/if}
					</Button>
				{/each}
			</div>
			{#if q.ifNoAnswer}<p class="text-xs text-muted-foreground [overflow-wrap:anywhere]">{t('ifNoAnswer', q.ifNoAnswer)}</p>{/if}
		</div>
	</div>
</article>
