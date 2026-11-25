<script lang="ts">
	import { Button } from '$lib/components/ui/button';
	import { Badge } from '$lib/components/ui/badge';
	import { Textarea } from '$lib/components/ui/textarea';
	import * as Select from '$lib/components/ui/select';
	import * as Tabs from '$lib/components/ui/tabs';
	import FileTextIcon from '@lucide/svelte/icons/file-text';
	import GitPullRequestIcon from '@lucide/svelte/icons/git-pull-request';
	import ListChecksIcon from '@lucide/svelte/icons/list-checks';
	import InboxIcon from '@lucide/svelte/icons/inbox';
	import CircleDotIcon from '@lucide/svelte/icons/circle-dot';
	import SaveIcon from '@lucide/svelte/icons/save';
	import UndoIcon from '@lucide/svelte/icons/undo-2';
	import RotateIcon from '@lucide/svelte/icons/rotate-ccw';
	import CheckIcon from '@lucide/svelte/icons/circle-check';
	import AlertIcon from '@lucide/svelte/icons/triangle-alert';
	import WorkflowIcon from '@lucide/svelte/icons/workflow';
	import { t } from '$lib/i18n';
	import { session } from '$lib/session.svelte';
	import { router, go } from '$lib/router.svelte';
	import { loader } from '$lib/loader.svelte';
	import { act } from '$lib/app/act';
	import { dialog } from '$lib/app/dialog.svelte';
	import { documents, type DocumentView } from '$lib/rest';
	import PageHeader from '$lib/app/PageHeader.svelte';
	import Section from '$lib/app/Section.svelte';
	import Loading from '$lib/app/Loading.svelte';
	import Markdown from '$lib/app/Markdown.svelte';

	// The texts Barnraise writes for people, as templates each team rewrites:
	// pick a document and the items it applies to, edit, see it rendered.
	const ICONS = { pr: GitPullRequestIcon, plan: ListChecksIcon, item: InboxIcon, issue: CircleDotIcon } as Record<string, typeof FileTextIcon>;
	const FIELDS: [string, string][] = [
		['{{.Item.ID}}', 'docFieldId'], ['{{.Item.Title}}', 'docFieldTitle'], ['{{.Item.Kind}}', 'docFieldKind'], ['{{.Item.Priority}}', 'docFieldPriority'],
		['{{.Item.Area}}', 'docFieldArea'], ['{{.Item.Goal}}', 'docFieldGoal'], ['{{.Item.Body}}', 'docFieldBody'], ['{{.Criteria}}', 'docFieldCriteria'],
		['{{bullets .Preserve}}', 'docFieldPreserve'], ['{{section .Plan "Risks"}}', 'docFieldSection'],
		['{{range .Diagrams}}\n**{{.Title}}**\n\n{{mermaid .}}\n{{end}}', 'docFieldDiagrams'], ['{{.Default}}', 'docFieldDefault'],
		['{{.Proposal.Problem}}', 'docFieldProblem'], ['{{if eq .DiagramsMode "always"}}…{{end}}', 'docFieldMode']
	];

	const project = $derived(session.project);
	const docs = loader(() => documents.list(project));
	let firstRead = true;
	$effect(() => {
		void project;
		if (firstRead) return void (firstRead = false);
		docs.reload();
	});
	let name = $state(router.current.args[0] || 'pr');
	let variant = $state('');
	let text = $state('');
	let loadedFor = $state('');
	let saved = $state<string | null>(null); // the file's text; null when the project has none
	let preview = $state<{ text: string; error?: string } | null>(null);
	let previewing = $state(false);
	let busy = $state(false);
	let ref = $state<HTMLTextAreaElement | null>(null);

	const doc = $derived(docs.data?.documents.find((d) => d.name === name));
	const fileOf = (d: DocumentView | undefined, v: string) => d?.files.find((f) => f.variant === v);
	const general = $derived(fileOf(doc, ''));
	const isArea = $derived(!!variant && !!docs.data?.areas.includes(variant));
	const dirty = $derived(text !== (saved ?? inherited()));

	// What a variant starts from: the project's general file, else the built-in one.
	function inherited() {
		return (variant && general?.text) || doc?.built_in || '';
	}

	// Load the text when the document or the variant changes (not on every read).
	$effect(() => {
		const key = project + '/' + name + '/' + variant;
		if (!doc || loadedFor === key) return;
		const f = fileOf(doc, variant);
		saved = f ? f.text : null;
		text = f ? f.text : inherited();
		loadedFor = key;
	});

	// Live preview, a moment after the last keystroke.
	$effect(() => {
		const src = text, n = name, v = variant, area = isArea;
		if (!doc) return;
		previewing = true;
		const h = setTimeout(async () => {
			try {
				preview = await documents.preview(project, n, src, area ? v : '', !area ? v : '');
			} catch (e) {
				preview = { text: '', error: String((e as Error).message || e) };
			} finally {
				previewing = false;
			}
		}, 300);
		return () => clearTimeout(h);
	});

	function pick(n: string) {
		if (n === name) return;
		const leave = () => {
			name = n;
			variant = '';
			go('documents', n);
		};
		if (dirty) confirmDiscard(leave);
		else leave();
	}
	function confirmDiscard(then: () => void) {
		dialog.open({ title: t('docDiscardTitle'), description: t('docDiscardHelp'), fields: [], submit: t('docDiscard'), danger: true, onSubmit: async () => (then(), '') });
	}

	function insert(snippet: string) {
		const el = ref;
		if (!el) {
			text += snippet;
			return;
		}
		const a = el.selectionStart, b = el.selectionEnd;
		text = text.slice(0, a) + snippet + text.slice(b);
		requestAnimationFrame(() => {
			el.focus();
			el.selectionStart = el.selectionEnd = a + snippet.length;
		});
	}

	async function save() {
		busy = true;
		const r = await act(() => documents.save(project, name, variant, text), (f) => t('docSaved', f.path));
		if (r) {
			saved = r.text;
			docs.reload();
		}
		busy = false;
	}
	function reset() {
		dialog.open({
			title: t('docResetTitle'),
			description: variant ? t('docResetVariantHelp') : t('docResetHelp'),
			fields: [],
			submit: t('docReset'),
			danger: true,
			onSubmit: async () => {
				await documents.remove(project, name, variant);
				saved = null;
				loadedFor = '';
				docs.reload();
				return t('docResetDone');
			}
		});
	}

	const variantLabel = (v: string) => (!v ? t('docGeneral') : (docs.data?.areas.includes(v) ? t('docArea') : t('docKind')) + ': ' + v);
	const modeText = $derived(docs.data ? t('docMode_' + docs.data.diagrams) : '');
</script>

<PageHeader title={t('docTitle')} icon={FileTextIcon} meta={[project, docs.data?.dir]} subtitle={t('docHelp')}>
	{#snippet badges()}
		{#if docs.data}<Badge variant="outline" class="gap-1"><WorkflowIcon class="size-3" />{t('docDiagramsBadge', modeText)}</Badge>{/if}
	{/snippet}
</PageHeader>

<Loading state={docs}>
	{#if docs.data}
		<div class="grid gap-4 lg:grid-cols-[15rem_1fr]">
			<nav class="space-y-1" aria-label={t('docTitle')}>
				{#each docs.data.documents as d (d.name)}
					{@const Icon = ICONS[d.name] || FileTextIcon}
					<button type="button" onclick={() => pick(d.name)}
						class="flex w-full items-start gap-2.5 rounded-lg border px-3 py-2.5 text-left transition-colors {d.name === name ? 'border-primary/40 bg-primary/5' : 'border-transparent hover:bg-muted/60'}">
						<Icon class="mt-0.5 size-4 shrink-0 {d.name === name ? 'text-primary' : 'text-muted-foreground'}" />
						<span class="min-w-0">
							<span class="block text-sm font-medium">{t('docName_' + d.name)}</span>
							<span class="block text-xs text-muted-foreground">{t('docDesc_' + d.name)}</span>
							<span class="mt-1 flex flex-wrap gap-1">
								{#if d.files.length}<Badge class="h-4 px-1.5 text-[10px]">{t('docCustom', d.files.length)}</Badge>{:else}<Badge variant="secondary" class="h-4 px-1.5 text-[10px]">{t('docBuiltIn')}</Badge>{/if}
							</span>
						</span>
					</button>
				{/each}
				<p class="px-3 pt-3 text-xs leading-relaxed text-muted-foreground">{t('docModeHelp')}</p>
			</nav>

			{#if doc}
				<div class="min-w-0 space-y-4">
					<div class="flex flex-wrap items-center gap-2">
						<Select.Root type="single" value={variant} onValueChange={(v) => (dirty ? confirmDiscard(() => (variant = v)) : (variant = v))}>
							<Select.Trigger size="sm" class="h-8 min-w-44" aria-label={t('docAppliesTo')}>{variantLabel(variant)}</Select.Trigger>
							<Select.Content>
								<Select.Item value="" label={t('docGeneral')} />
								{#if docs.data.areas.length}
									<Select.Group><Select.GroupHeading>{t('docAreas')}</Select.GroupHeading>
										{#each docs.data.areas as a (a)}<Select.Item value={a} label={a + (fileOf(doc, a) ? ' •' : '')} />{/each}
									</Select.Group>
								{/if}
								<Select.Group><Select.GroupHeading>{t('docKinds')}</Select.GroupHeading>
									{#each docs.data.kinds as k (k)}<Select.Item value={k} label={k + (fileOf(doc, k) ? ' •' : '')} />{/each}
								</Select.Group>
							</Select.Content>
						</Select.Root>
						<span class="text-xs text-muted-foreground">
							{#if saved !== null}{t('docFileIs', fileOf(doc, variant)?.path || '')}{:else if variant && general}{t('docInheritsGeneral')}{:else}{t('docUsesBuiltIn')}{/if}
						</span>
						<div class="ml-auto flex items-center gap-1.5">
							{#if dirty}<Button variant="ghost" size="sm" onclick={() => (text = saved ?? inherited())}><UndoIcon class="size-3.5" />{t('docUndo')}</Button>{/if}
							{#if saved !== null}<Button variant="ghost" size="sm" onclick={reset}><RotateIcon class="size-3.5" />{t('docReset')}</Button>{/if}
							<Button size="sm" disabled={busy || !dirty || !!preview?.error} onclick={save}><SaveIcon class="size-3.5" />{t('docSave')}</Button>
						</div>
					</div>

					<div class="grid min-w-0 gap-4 2xl:grid-cols-2">
						<Section title={t('docTemplate')} sub={dirty ? t('docUnsaved') : ''} pad={false}>
							<Textarea bind:ref bind:value={text} spellcheck={false} aria-label={t('docTemplate')}
								class="num min-h-[28rem] resize-y rounded-none border-0 bg-transparent p-4 text-xs leading-relaxed shadow-none focus-visible:ring-0 dark:bg-transparent" />
							<div class="border-t bg-muted/30 px-4 py-3">
								<p class="mb-2 text-[11px] font-medium tracking-wide text-muted-foreground uppercase">{t('docInsert')}</p>
								<div class="flex flex-wrap gap-1.5">
									{#each FIELDS as [snippet, label] (snippet)}
										<button type="button" onclick={() => insert(snippet)} title={snippet}
											class="rounded-md border bg-background px-2 py-0.5 text-[11px] text-foreground/80 transition-colors hover:border-primary/50 hover:text-foreground">{t(label)}</button>
									{/each}
								</div>
							</div>
						</Section>

						<Section title={t('docPreview')} sub={t('docPreviewHelp')}>
							{#snippet right()}
								{#if previewing}<span class="text-xs text-muted-foreground">…</span>
								{:else if preview?.error}<span class="flex items-center gap-1 text-xs text-st-fail"><AlertIcon class="size-3.5" />{t('docBroken')}</span>
								{:else if preview}<span class="flex items-center gap-1 text-xs text-st-done"><CheckIcon class="size-3.5" />{t('docRenders')}</span>{/if}
							{/snippet}
							{#if preview?.error}
								<p class="num mb-3 rounded-md bg-st-fail/10 px-3 py-2 text-xs text-st-fail">{preview.error}</p>
							{/if}
							{#if preview?.text}
								<Tabs.Root value="rendered">
									<Tabs.List class="mb-3">
										<Tabs.Trigger value="rendered">{t('docRendered')}</Tabs.Trigger>
										<Tabs.Trigger value="md">{t('docMarkdown')}</Tabs.Trigger>
									</Tabs.List>
									<Tabs.Content value="rendered"><Markdown text={preview.text} /></Tabs.Content>
									<Tabs.Content value="md"><pre class="num overflow-x-auto rounded-lg bg-muted/60 p-3 text-xs">{preview.text}</pre></Tabs.Content>
								</Tabs.Root>
							{/if}
						</Section>
					</div>
					<p class="text-xs text-muted-foreground">{t('docCommitHint')}</p>
				</div>
			{/if}
		</div>
	{/if}
</Loading>
