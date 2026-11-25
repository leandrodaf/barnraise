<script lang="ts">
	import { Button } from '$lib/components/ui/button';
	import CopyIcon from '@lucide/svelte/icons/copy';
	import FileCode from '@lucide/svelte/icons/file-code-2';
	import Pencil from '@lucide/svelte/icons/pencil';
	import { toast } from 'svelte-sonner';
	import { t } from '$lib/i18n';
	import { href } from '$lib/router.svelte';
	import { loader } from '$lib/loader.svelte';
	import { documents } from '$lib/rest';
	import Section from './Section.svelte';
	import Markdown from './Markdown.svelte';
	import Loading from './Loading.svelte';
	import Empty from './Empty.svelte';
	import BeforeAfter from './BeforeAfter.svelte';

	// An item's documents as Barnraise writes them now: the design before and
	// after, the plan, and the body of its pull request.
	let { project, id }: { project: string; id: string } = $props();
	const docs = loader(() => documents.ofItem(project, id));
	let raw = $state(false);

	async function copy(text: string) {
		try {
			await navigator.clipboard.writeText(text);
			toast.success(t('docCopied'));
		} catch {
			/* clipboard refused */
		}
	}
</script>

<Loading state={docs}>
	{#if docs.data}
		{@const d = docs.data}
		<div class="space-y-4">
			<Section title={t('docBeforeAfter')} count={d.diagrams.length || undefined} sub={t('docBeforeAfterHelp')}>
				{#if d.diagrams.length}
					<BeforeAfter diagrams={d.diagrams} />
				{:else}
					<Empty text={d.plan ? t('docNoDiagrams') : t('docNoPlan')} />
				{/if}
			</Section>
			{#if d.plan}
				<Section title="plan.md" sub={t('docPlanHelp')}>
					{#snippet right()}<Button variant="ghost" size="sm" onclick={() => copy(d.plan)}><CopyIcon class="size-3.5" />{t('docCopy')}</Button>{/snippet}
					<Markdown text={d.plan.replace(/^#{1,6}\s+(before and after|antes e depois)\s*\n[\s\S]*?(?=^#{1,2}\s|(?![\s\S]))/im, '')} />
				</Section>
			{/if}
			<Section title={t('docPRBody')} sub={d.template ? t('docFrom', d.template) : t('docFromBuiltIn')}>
				{#snippet right()}
					<div class="flex items-center gap-1">
						<Button variant="ghost" size="sm" onclick={() => (raw = !raw)}><FileCode class="size-3.5" />{raw ? t('docRendered') : t('docMarkdown')}</Button>
						<Button variant="ghost" size="sm" onclick={() => copy(d.pr_body)}><CopyIcon class="size-3.5" />{t('docCopy')}</Button>
						<Button variant="ghost" size="sm" href={href('documents', 'pr')}><Pencil class="size-3.5" />{t('docEditTemplate')}</Button>
					</div>
				{/snippet}
				{#if d.warning}<p class="mb-3 rounded-md bg-st-needs/10 px-3 py-2 text-xs text-st-needs">{d.warning}</p>{/if}
				{#if raw}<pre class="num overflow-x-auto rounded-lg bg-muted/60 p-3 text-xs">{d.pr_body}</pre>{:else}<Markdown text={d.pr_body} />{/if}
			</Section>
		</div>
	{/if}
</Loading>
