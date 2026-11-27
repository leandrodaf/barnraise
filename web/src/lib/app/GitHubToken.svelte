<script lang="ts">
	// A project's GitHub token, without the CLI: GitHub's form opens with
	// every permission filled in, the token is pasted back, each permission
	// is tested on every machine and the token is kept in each keychain.
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Badge } from '$lib/components/ui/badge';
	import ExternalLinkIcon from '@lucide/svelte/icons/external-link';
	import CheckIcon from '@lucide/svelte/icons/check';
	import XIcon from '@lucide/svelte/icons/x';
	import CircleHelpIcon from '@lucide/svelte/icons/circle-help';
	import ShieldCheckIcon from '@lucide/svelte/icons/shield-check';
	import { toast } from 'svelte-sonner';
	import { api } from '$lib/api';
	import { t } from '$lib/i18n';
	import { session } from '$lib/session.svelte';
	import { cluster } from '$lib/cluster.svelte';
	import Section from '$lib/app/Section.svelte';
	import type { GitHubTokenResponse, GitHubTokenSetup } from '$lib/gen/barnraise/v1/barnraise_pb';

	// Only the projects on GitHub: the others have no token to set here.
	let setups = $state<Record<string, GitHubTokenSetup>>({});
	let ready = $state(false);
	let project = $state('');
	const setup = $derived(setups[project] || null);
	const ghProjects = $derived(Object.keys(setups));
	let token = $state('');
	let busy = $state('');
	let result = $state<GitHubTokenResponse | null>(null);

	async function loadAll(names: string[]) {
		const found: Record<string, GitHubTokenSetup> = {};
		await Promise.all(
			names.map(async (p) => {
				try {
					found[p] = await api.getGitHubTokenSetup({ project: p });
				} catch {
					// Not on GitHub (or not reachable): not listed.
				}
			})
		);
		setups = found;
		if (!found[project]) project = names.find((p) => found[p] && !cluster.all?.projects.find((x) => x.project === p)?.paused) || names.find((p) => found[p]) || '';
		ready = true;
	}
	async function reloadOne(p: string) {
		try {
			setups = { ...setups, [p]: await api.getGitHubTokenSetup({ project: p }) };
		} catch {
			/* keep what was shown */
		}
	}
	$effect(() => {
		loadAll(session.projects);
	});
	$effect(() => {
		project;
		result = null;
	});

	async function run(testOnly: boolean) {
		busy = testOnly ? 'test' : 'install';
		result = null;
		try {
			result = await api.installGitHubToken({ project, token: token.trim(), testOnly });
			const failed = result.machines.filter((m) => m.error);
			if (failed.length) toast.error(failed.map((m) => m.machine + ': ' + m.error).join('\n'));
			else if (testOnly) toast.success(t('ghtTestOk'));
			else if (result.installed) {
				toast.success(t('ghtInstalled', result.machines.map((m) => m.machine).join(', ')));
				token = '';
				reloadOne(project);
			}
		} catch (e) {
			toast.error(e instanceof Error ? e.message : String(e));
		}
		busy = '';
	}
</script>

<Section title={t('ghtTitle')} sub={t('ghtHelp')}>
	{#snippet right()}
		{#if ghProjects.length > 1}
			<select class="rounded-md border bg-background px-2 py-1 text-sm" bind:value={project}>
				{#each ghProjects as p (p)}<option value={p}>{p}</option>{/each}
			</select>
		{/if}
	{/snippet}
	{#if ready && !setup}
		<p class="text-sm text-muted-foreground">{t('ghtNone')}</p>
	{:else if setup}
		<p class="mb-3 text-xs text-muted-foreground">{t('ghtCurrent', setup.repo)} <span class="text-foreground">{setup.current}</span></p>
		<ol class="space-y-4 text-sm">
			<li>
				<p class="mb-1.5 font-medium">{t('ghtStep1')}</p>
				<Button size="sm" href={setup.url} target="_blank" rel="noreferrer"><ExternalLinkIcon />{t('ghtOpen')}</Button>
				<p class="mt-1.5 text-xs text-muted-foreground">{t('ghtStep1Help', setup.repo)}</p>
				<ul class="mt-2 grid gap-x-4 gap-y-0.5 text-xs sm:grid-cols-2">
					{#each setup.permissions as p (p.name)}
						<li><code class="num rounded bg-muted px-1 py-px text-[10px]">{p.name}: {p.access}</code> <span class="text-muted-foreground">{p.why}</span></li>
					{/each}
				</ul>
			</li>
			<li>
				<p class="mb-1.5 font-medium">{t('ghtStep2')}</p>
				<div class="flex flex-wrap gap-2">
					<Input type="password" autocomplete="off" placeholder="github_pat_…" bind:value={token} class="min-w-0 flex-1 font-mono text-xs" />
					<Button size="sm" variant="outline" disabled={!token.trim() || !!busy} onclick={() => run(true)}>{busy === 'test' ? t('ghtTesting') : t('ghtTest')}</Button>
					<Button size="sm" disabled={!token.trim() || !!busy} onclick={() => run(false)}><ShieldCheckIcon />{busy === 'install' ? t('ghtInstalling') : t('ghtInstall')}</Button>
				</div>
				<p class="mt-1.5 text-xs text-muted-foreground">{t('ghtStep2Help')}</p>
			</li>
		</ol>
		{#if result}
			<div class="mt-4 space-y-3 border-t pt-3">
				{#each result.machines as m (m.machine)}
					<div>
						<p class="flex flex-wrap items-center gap-2 text-sm font-medium">
							{m.machine}
							{#if m.installed}<Badge>{t('ghtBadgeInstalled')}</Badge>{:else if !m.error}<Badge variant="secondary">{t('ghtBadgeTested')}</Badge>{:else}<Badge variant="destructive">{t('ghtBadgeFailed')}</Badge>{/if}
							{#if m.kind}<span class="text-[11px] font-normal text-muted-foreground">{m.kind}{m.expires ? ' · ' + t('ghtExpires', m.expires) : ''}</span>{/if}
						</p>
						{#if m.error}<p class="text-xs text-destructive">{m.error}</p>{/if}
						<ul class="mt-1 grid gap-x-4 gap-y-0.5 text-xs sm:grid-cols-2">
							{#each m.checks as c (c.name)}
								<li class="flex items-start gap-1.5">
									{#if !c.testable}<CircleHelpIcon class="mt-0.5 size-3.5 shrink-0 text-muted-foreground" />{:else if c.ok}<CheckIcon class="mt-0.5 size-3.5 shrink-0 text-st-done" />{:else}<XIcon class="mt-0.5 size-3.5 shrink-0 text-destructive" />{/if}
									<span><code class="num text-[10px]">{c.name}: {c.access}</code>{#if c.detail} <span class="text-muted-foreground">{c.detail}</span>{/if}</span>
								</li>
							{/each}
						</ul>
					</div>
				{/each}
			</div>
		{/if}
	{/if}
</Section>
