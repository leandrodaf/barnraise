<script lang="ts">
	import { Badge } from '$lib/components/ui/badge';
	import { Button } from '$lib/components/ui/button';
	import type { Item, TimelineEntry } from '$lib/gen/barnraise/v1/barnraise_pb';
	import { api } from '$lib/api';
	import { t, tm } from '$lib/i18n';
	import { compact, dur, when } from '$lib/format';
	import { dialog } from './dialog.svelte';
	import Empty from './Empty.svelte';

	let { item, entries, project }: { item: Item; entries: TimelineEntry[]; project: string } = $props();
	const canRedo = $derived(item.status !== 'integrated' && item.status !== 'closed');

	const failed = (e: TimelineEntry, p: any) =>
		(p.check && !p.check.passed) || (p.review && /rework|reject/i.test(p.review.decision || '')) || e.type === 'rework.started' || e.type === 'item.blocked';
	const verdict = (x: string) => (x === 'pass' ? t('passed') : x === 'fail' ? t('failed') : x || '—');
	const tone = (good: boolean, bad: boolean) => (good ? 'bg-success/15 text-success' : bad ? 'bg-destructive/10 text-destructive' : '');

	function redo(step: string) {
		dialog.open({
			title: t('redoTitle', item.id, step),
			fields: [{ name: 'why', label: t('redoWhy'), type: 'textarea', required: true }],
			submit: t('redoConfirm'),
			onSubmit: async (v) => {
				await api.redo({ project, id: item.id, step, why: v.why });
				return t('redone', item.id, step);
			}
		});
	}
</script>

{#if !entries.length}
	<Empty text={t('noTimeline')} />
{:else}
	<ol class="relative space-y-4 border-l pl-5">
		{#each entries as e (e.seq)}
			{@const p = (e.payload || {}) as any}
			<li class="relative">
				<span class="absolute -left-[1.6rem] top-1.5 size-2.5 rounded-full border-2 border-background {failed(e, p) ? 'bg-destructive' : 'bg-primary/70'}" aria-hidden="true"></span>
				<div class="flex flex-wrap items-center gap-2 text-sm">
					<strong>{tm('events', e.type)}</strong>
					{#if e.step}<Badge variant="outline">{e.step}</Badge>{/if}
					<time class="text-xs text-muted-foreground" datetime={e.time}>{when(e.time)}</time>
					{#if e.actor}<span class="text-xs text-muted-foreground">{e.actor}{e.machine ? ' @ ' + e.machine : ''}</span>{/if}
				</div>
				{#if e.summary}<p class="mt-1 text-sm">{e.summary}</p>{/if}
				<div class="mt-1 space-y-1 text-sm">
					{#if p.delivery}
						<p><Badge variant="secondary" class={tone(/DONE$/.test(p.delivery.status || ''), false)}>{p.delivery.status}</Badge> {p.delivery.summary}</p>
						{#if p.delivery.reason}<p class="text-muted-foreground">{p.delivery.reason}</p>{/if}
						{#if p.delivery.concerns?.length}<p class="text-xs text-muted-foreground">{t('concerns')}</p><ul class="list-disc pl-5">{#each p.delivery.concerns as c, i (i)}<li>{c}</li>{/each}</ul>{/if}
					{/if}
					{#if p.review}
						{@const r = p.review}
						<p class="flex flex-wrap items-center gap-1.5">
							{t('verdictCriteria', '')}<Badge variant="secondary" class={tone(r.criteria_verdict === 'pass', r.criteria_verdict === 'fail')}>{verdict(r.criteria_verdict)}</Badge>
							{t('verdictCode', '')}<Badge variant="secondary" class={tone(r.code_verdict === 'pass', r.code_verdict === 'fail')}>{verdict(r.code_verdict)}</Badge>
							{t('decision', '')}<Badge variant="secondary" class={tone(r.decision === 'APPROVE', r.decision !== 'APPROVE')}>{r.decision}</Badge>
							{#if r.reviewer}<span class="text-xs text-muted-foreground">{r.reviewer}{r.family ? ' · ' + r.family : ''}</span>{/if}
						</p>
						{#if r.summary}<p>{r.summary}</p>{/if}
						{#if r.findings?.length}
							<p class="text-xs text-muted-foreground">{t('findings')}</p>
							<ul class="list-disc space-y-0.5 pl-5">
								{#each r.findings as f, i (i)}<li>{#if f.severity}<Badge variant="outline" class="mr-1">{f.severity}</Badge>{/if}{#if f.file}<span class="mr-1 font-mono text-xs">{f.file}{f.line ? ':' + f.line : ''}</span>{/if}{f.text}</li>{/each}
							</ul>
						{/if}
					{/if}
					{#if p.check}
						{@const c = p.check}
						<p>
							<Badge variant="secondary" class={tone(c.passed, !c.passed)}>{c.passed ? t('passed') : t('failed')}</Badge>
							{#if c.flaky}<Badge variant="secondary" class="bg-warning/20">{t('flaky')}</Badge>{/if}
							{t('checkName', c.check)}{#if c.duration}<span class="text-muted-foreground"> · {dur(Number(c.duration) / 1e9)}</span>{/if}{#if c.runs_on}<span class="text-muted-foreground"> · {c.runs_on}</span>{/if}
						</p>
						{#if c.summary}<p>{c.summary}</p>{/if}
					{/if}
					{#if p.rework}<p>{t('reworkLine', p.rework.from, p.rework.to, p.rework.by, p.rework.reason)}</p>{/if}
					{#if p.usage}
						{@const u = p.usage}
						<p class="text-xs text-muted-foreground">{t('tokens')}: {t('tokenLine', compact(u.input), compact(u.output), compact(u.cache_read))}{u.model ? ' · ' + u.model : ''}{u.role ? ' · ' + u.role : ''}{u.duration ? ' · ' + dur(Number(u.duration) / 1e9) : ''}</p>
					{/if}
					{#if p.question}<p>{p.question.decision}{p.question.answer ? ' → ' + p.question.answer : ''}</p>{/if}
					{#if p.note && !p.delivery && !p.review && !p.check && !p.rework && !p.usage && !p.question}<p class="text-muted-foreground">{p.note}</p>{/if}
				</div>
				{#if canRedo && e.step && e.type === 'item.step.started'}
					<Button size="sm" variant="ghost" class="mt-1 h-7" onclick={() => redo(e.step)}>{t('redoHere')}</Button>
				{/if}
			</li>
		{/each}
	</ol>
{/if}
