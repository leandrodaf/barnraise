<script lang="ts">
	// The one step that puts a project on this machine: paste where it is.
	// The daemon clones it, writes its barnraise.yaml from what it holds and
	// registers it (AddProject); a private repository asks for a token.
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Spinner } from '$lib/components/ui/spinner';
	import FolderGitIcon from '@lucide/svelte/icons/folder-git-2';
	import KeyIcon from '@lucide/svelte/icons/key-round';
	import ExternalIcon from '@lucide/svelte/icons/external-link';
	import { toast } from 'svelte-sonner';
	import { api } from '$lib/api';
	import { t } from '$lib/i18n';
	import { locale } from '$lib/locale.svelte';
	import { session } from '$lib/session.svelte';
	import { go } from '$lib/router.svelte';
	import { cluster } from '$lib/cluster.svelte';
	import { loginPanel } from './login.svelte';
	import GitHubToken from './GitHubToken.svelte';
	import CheckIcon from '@lucide/svelte/icons/circle-check';
	import CircleIcon from '@lucide/svelte/icons/circle';
	import LogInIcon from '@lucide/svelte/icons/log-in';

	let { onDone }: { onDone?: (project: string) => void } = $props();

	let source = $state('');
	let token = $state('');
	let busy = $state(false);
	let elapsed = $state(0);
	let error = $state('');
	let needsToken = $state(false);
	let tokenUrl = $state('');
	let detail = $state('');
	// After the add: what is left before agents work, in one place.
	let added = $state<{ project: string; items: string[] } | null>(null);
	const hasAccount = $derived((cluster.data?.projects || []).some((p) => p.accounts.some((a) => a.status === 'ok' || a.status === 'limited')));
	const needsGitHub = $derived(!!added?.items.some((p) => /github|push/i.test(p)));
	const others = $derived((added?.items || []).filter((p) => !/github|push/i.test(p)));

	async function add(e: SubmitEvent) {
		e.preventDefault();
		if (!source.trim() || busy) return;
		busy = true;
		error = '';
		elapsed = 0;
		const began = Date.now();
		const tick = setInterval(() => (elapsed = Math.round((Date.now() - began) / 1000)), 500);
		try {
			const r = await api.addProject({ source: source.trim(), token: token.trim(), language: locale.lang });
			if (r.needsToken) {
				needsToken = true;
				tokenUrl = r.tokenUrl;
				detail = r.detail;
				return;
			}
			needsToken = false;
			token = '';
			await session.readProjects();
			session.setScope(r.project);
			toast.success(t('apAdded', r.project));
			if (r.notReady.length || !hasAccount) {
				added = { project: r.project, items: r.notReady };
				return;
			}
			finish(r.project);
		} catch (err) {
			error = String((err as Error)?.message || err).replace(/^\[\w+\]\s*/, '');
		} finally {
			clearInterval(tick);
			busy = false;
		}
	}

	function finish(project: string) {
		go('home');
		onDone?.(project);
	}
</script>

{#snippet step(done: boolean, label: string)}
	<p class="flex items-center gap-2 text-sm font-medium">
		{#if done}<CheckIcon class="size-4 text-emerald-500" />{:else}<CircleIcon class="size-4 text-muted-foreground/60" />{/if}{label}
	</p>
{/snippet}

{#if added}
	<div class="space-y-5">
		{@render step(true, t('apStepAdded', added.project))}
		<div class="space-y-2">
			{@render step(hasAccount, t('apStepAccount'))}
			{#if !hasAccount}
				<p class="pl-6 text-xs text-muted-foreground">{t('apStepAccountHow')}</p>
				<div class="pl-6"><Button size="sm" onclick={() => loginPanel.show('', 'claude')}><LogInIcon />{t('apConnectClaude')}</Button></div>
			{/if}
		</div>
		{#if needsGitHub}
			<div class="space-y-2">
				{@render step(false, t('apStepGitHub'))}
				<p class="pl-6 text-xs text-muted-foreground">{t('apStepGitHubHow')}</p>
				<div class="pl-6"><GitHubToken /></div>
			</div>
		{/if}
		{#if others.length}
			<div class="space-y-2">
				{@render step(false, t('apStepOther'))}
				<ul class="list-disc space-y-1 pl-10 text-xs text-muted-foreground">
					{#each others as p (p)}<li>{p}</li>{/each}
				</ul>
			</div>
		{/if}
		{#if added.items.length}<p class="text-xs text-muted-foreground">{t('apAutoResume')}</p>{/if}
		<div class="flex gap-2">
			<Button onclick={() => finish(added!.project)}>{t('apOpenProject')}</Button>
			<Button variant="outline" onclick={() => { added = null; source = ''; }}>{t('apAnother')}</Button>
		</div>
	</div>
{:else}
	<form class="space-y-4" onsubmit={add}>
		<div class="grid gap-1.5">
			<Label for="ap-source">{t('apSource')}</Label>
			<div class="flex gap-2">
				<Input id="ap-source" class="flex-1" placeholder="github.com/you/your-project" bind:value={source} disabled={busy} autocomplete="off" spellcheck={false} />
				<Button type="submit" disabled={busy || !source.trim()}>
					{#if busy}<Spinner />{t('apAdding', elapsed)}{:else}<FolderGitIcon />{t('apAdd')}{/if}
				</Button>
			</div>
			<p class="text-xs text-muted-foreground">{busy ? t('apAddingWhy') : t('apSourceHelp')}</p>
		</div>
		{#if needsToken}
			<div class="space-y-3 rounded-lg border border-st-needs/40 bg-st-needs/5 p-4">
				<p class="flex items-center gap-2 text-sm font-medium"><KeyIcon class="size-4" />{t('apNeedsToken')}</p>
				<p class="text-xs text-muted-foreground">{tokenUrl ? t('apTokenHowGitHub') : t('apTokenHow')}</p>
				{#if tokenUrl}
					<Button variant="outline" size="sm" href={tokenUrl} target="_blank" rel="noopener noreferrer"><ExternalIcon />{t('apCreateToken')}</Button>
				{/if}
				<div class="grid gap-1.5">
					<Label for="ap-token">{t('tokenLabel')}</Label>
					<Input id="ap-token" type="password" placeholder="github_pat_…" bind:value={token} disabled={busy} autocomplete="off" />
				</div>
				{#if detail}
					<details class="text-xs text-muted-foreground">
						<summary class="cursor-pointer">{t('dkShowLog')}</summary>
						<pre class="mt-1 max-h-40 overflow-auto rounded bg-muted p-2 whitespace-pre-wrap">{detail}</pre>
					</details>
				{/if}
			</div>
		{/if}
		{#if error}<p class="text-sm text-destructive" role="alert">{error}</p>{/if}
	</form>
{/if}
