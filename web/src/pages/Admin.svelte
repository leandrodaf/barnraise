<script lang="ts">
	import { Button } from '$lib/components/ui/button';
	import { Badge } from '$lib/components/ui/badge';
	import SettingsIcon from '@lucide/svelte/icons/settings-2';
	import FolderPlusIcon from '@lucide/svelte/icons/folder-plus';
	import RefreshIcon from '@lucide/svelte/icons/refresh-cw';
	import GitMergeIcon from '@lucide/svelte/icons/git-merge';
	import ScanSearchIcon from '@lucide/svelte/icons/scan-search';
	import KeyIcon from '@lucide/svelte/icons/key';
	import PlusIcon from '@lucide/svelte/icons/plus';
	import CopyIcon from '@lucide/svelte/icons/copy';
	import PowerIcon from '@lucide/svelte/icons/power';
	import LogOutIcon from '@lucide/svelte/icons/log-out';
	import XIcon from '@lucide/svelte/icons/x';
	import { toast } from 'svelte-sonner';
	import { api } from '$lib/api';
	import { t } from '$lib/i18n';
	import { ago, day, when } from '$lib/format';
	import { session } from '$lib/session.svelte';
	import { cluster } from '$lib/cluster.svelte';
	import { loader } from '$lib/loader.svelte';
	import { act } from '$lib/app/act';
	import { dialog } from '$lib/app/dialog.svelte';
	import PageHeader from '$lib/app/PageHeader.svelte';
	import PageGrid from '$lib/app/PageGrid.svelte';
	import Section from '$lib/app/Section.svelte';
	import Loading from '$lib/app/Loading.svelte';
	import Empty from '$lib/app/Empty.svelte';
	import Dot from '$lib/app/Dot.svelte';
	import GitHubToken from '$lib/app/GitHubToken.svelte';
	import { addProjectDialog } from '$lib/app/addproject.svelte';
	import AgentMCP from '$lib/app/AgentMCP.svelte';

	// What the CLI does to the daemon, from the panel: projects, tokens and
	// the cluster's daemons.
	const SCOPES = ['items:read', 'items:write', 'items:claim', 'deliveries:write', 'reviews:write', 'checks:report', 'approve', 'focus', 'pause',
		'proposals:write', 'proposals:decide', 'messages:write', 'owner:ask', 'owner:answer', 'tweaks:write', 'accounts:write', 'machines:write', 'admin',
		'events:read', 'usage:report', 'journal:write'];
	const PRESETS: Record<string, string[]> = {
		read: ['items:read', 'events:read'],
		owner: ['items:read', 'items:write', 'approve', 'focus', 'pause', 'proposals:decide', 'owner:answer', 'tweaks:write', 'events:read'],
		admin: ['*']
	};
	const tokens = loader(() => api.listTokens({}).then((r) => r.tokens));
	const peers = loader(() => api.listPeers({}).then((r) => r.peers));
	let secret = $state<{ id: string; name: string; secret: string } | null>(null);
	let busy = $state('');
	const projectOf = (name: string) => cluster.all?.projects.find((p) => p.project === name);

	async function run(key: string, fn: () => Promise<string>) {
		busy = key;
		await act(fn, (m) => m);
		busy = '';
	}
	const reload = (project: string) => run('reload' + project, async () => (await api.reloadProject({ project }), t('admReloaded', project)));
	const queue = (project: string) => run('queue' + project, async () => {
		const r = await api.processQueue({ project });
		return t('admQueueRan', project, r.integrated, r.items.length);
	});
	const sweep = (project: string) => run('sweep' + project, async () => {
		const r = (await api.sweep({ project })).result as Record<string, any> | undefined;
		return t('admSwept', project, (r?.candidates || []).length, (r?.verdicts || []).length, (r?.deferred || []).length);
	});

	function newToken() {
		dialog.open({
			title: t('tokNew'),
			description: t('tokNewHelp'),
			fields: [
				{ name: 'name', label: t('tokName'), required: true, placeholder: 'ci, phone, danilo…' },
				{ name: 'project', label: t('project'), type: 'select', value: '', options: [['', t('allProjects')], ...session.projects.map((p) => [p, p] as [string, string])] },
				{ name: 'preset', label: t('tokPreset'), type: 'select', value: 'read', options: [['read', t('tokPreset_read')], ['owner', t('tokPreset_owner')], ['admin', t('tokPreset_admin')], ['custom', t('tokPreset_custom')]] },
				{ name: 'scopes', label: t('tokScopes'), help: t('tokScopesHelp', SCOPES.join(', ')) },
				{ name: 'ttl', label: t('tokTtl'), placeholder: '30d', help: t('tokTtlHelp') }
			],
			submit: t('tokCreate'),
			onSubmit: async (v) => {
				const scopes = v.preset === 'custom' ? v.scopes.split(/[\s,]+/).filter(Boolean) : PRESETS[v.preset];
				if (!scopes.length) throw new Error(t('tokNoScopes'));
				const r = await api.createToken({ name: v.name.trim(), project: v.project, scopes, ttl: v.ttl.trim() });
				secret = { id: r.token?.id || '', name: v.name, secret: r.secret };
				tokens.reload();
				return t('tokCreated', v.name);
			}
		});
	}
	function revoke(id: string, name: string) {
		dialog.open({
			title: t('tokRevokeTitle', name),
			description: t('tokRevokeHelp'),
			fields: [],
			submit: t('tokRevoke'),
			danger: true,
			onSubmit: async () => {
				await api.revokeToken({ id });
				tokens.reload();
				return t('tokRevoked', name);
			}
		});
	}
	function stopPeers(names: string[]) {
		dialog.open({
			title: names.length ? t('admStopTitle', names.join(', ')) : t('admStopAllTitle'),
			description: t('admStopHelp'),
			fields: [],
			submit: t('admStop'),
			danger: true,
			onSubmit: async () => {
				const r = await api.stopPeers({ names });
				peers.reload();
				const bad = r.results.filter((x) => !x.stopped);
				if (bad.length) throw new Error(bad.map((x) => x.name + ': ' + x.error).join('; '));
				return t('admStopped', r.results.map((x) => x.name).join(', ') || '—');
			}
		});
	}
	function leave() {
		dialog.open({
			title: t('admLeaveTitle', session.machine),
			description: t('admLeaveHelp'),
			fields: [],
			submit: t('admLeave'),
			danger: true,
			onSubmit: async () => {
				const r = await api.leaveMachine({ project: '' });
				return t('admLeft', r.returned.length ? r.returned.join(', ') : '—');
			}
		});
	}
	const copy = (s: string) => navigator.clipboard?.writeText(s).then(() => toast.success(t('copied')));
</script>

<PageHeader title={t('navAdmin')} icon={SettingsIcon} meta={[session.machine, session.version, t('projectsN', session.projects.length)]} subtitle={t('adminHelp')}>
	{#snippet actions()}<Button size="sm" onclick={() => (addProjectDialog.open = true)}><FolderPlusIcon />{t('apAddTitle')}</Button>{/snippet}
</PageHeader>

<PageGrid>
	<Section title={t('projects')} count={session.projects.length} pad={false}>
		<ul class="divide-y">
			{#each session.projects as p (p)}
				{@const po = projectOf(p)}
				<li class="flex flex-wrap items-center gap-3 px-4 py-3">
					<div class="min-w-0 flex-1">
						<p class="flex items-center gap-2 font-medium"><Dot tone={po?.paused ? 'needs' : 'done'} />{p}{#if po?.paused}<Badge variant="secondary">{t('paused')}</Badge>{/if}{#if session.autopilot.includes(p)}<Badge variant="outline">{t('autopilot')}</Badge>{/if}</p>
						<p class="num text-[11px] text-muted-foreground">{po ? t('admProjectLine', po.itemsTotal, po.openQuestions, po.openProposals) : ''}</p>
					</div>
					<div class="flex flex-wrap gap-1.5">
						<Button size="sm" variant="outline" disabled={busy === 'reload' + p} onclick={() => reload(p)} title={t('admReloadHelp')}><RefreshIcon class={busy === 'reload' + p ? 'animate-spin' : ''} />{t('admReload')}</Button>
						<Button size="sm" variant="outline" disabled={busy === 'queue' + p} onclick={() => queue(p)} title={t('admQueueHelp')}><GitMergeIcon />{t('admQueue')}</Button>
						<Button size="sm" variant="outline" disabled={busy === 'sweep' + p} onclick={() => sweep(p)} title={t('admSweepHelp')}><ScanSearchIcon class={busy === 'sweep' + p ? 'animate-pulse' : ''} />{t('admSweep')}</Button>
					</div>
				</li>
			{/each}
		</ul>
	</Section>

	{#if secret}
		<Section title={t('tokSecretTitle', secret.name)} tone="border-st-needs/60">
			{#snippet right()}<button class="rounded p-1 hover:bg-muted" onclick={() => (secret = null)} aria-label={t('cancel')}><XIcon class="size-4" /></button>{/snippet}
			<p class="mb-2 text-sm text-st-needs">{t('tokSecretOnce')}</p>
			<div class="flex items-center gap-2"><code class="num min-w-0 flex-1 break-all rounded-md bg-muted px-2 py-1.5 text-xs">{secret.secret}</code><Button size="sm" onclick={() => copy(secret!.secret)}><CopyIcon />{t('copy')}</Button></div>
		</Section>
	{/if}

	<AgentMCP />

	<GitHubToken />

	<Section title={t('tokTitle')} count={tokens.data?.length} sub={t('tokHelp')} pad={false}>
		{#snippet right()}<Button size="sm" variant="outline" onclick={newToken}><PlusIcon />{t('tokNew')}</Button>{/snippet}
		<Loading state={tokens}>
			{#if !tokens.data?.length}<div class="p-4"><Empty /></div>{:else}
				<ul class="divide-y">
					{#each tokens.data as tk (tk.id)}
						<li class="flex flex-wrap items-start gap-3 px-4 py-2.5 {tk.revoked ? 'opacity-50' : ''}">
							<KeyIcon class="mt-1 size-4 shrink-0 text-muted-foreground" />
							<div class="min-w-0 flex-1">
								<p class="flex flex-wrap items-center gap-2 text-sm font-medium">{tk.name}<span class="num text-[11px] font-normal text-muted-foreground">{tk.id}</span>{#if tk.revoked}<Badge variant="destructive">{t('tokRevokedBadge')}</Badge>{/if}</p>
								<p class="num text-[11px] text-muted-foreground">{tk.project || t('allProjects')} · {t('tokCreatedAt', day(tk.created))}{tk.expires ? ' · ' + t('tokExpires', when(tk.expires)) : ' · ' + t('tokNoExpiry')}</p>
								<div class="mt-1 flex flex-wrap gap-1">{#each tk.scopes as s (s)}<code class="num rounded bg-muted px-1 py-px text-[10px]">{s}</code>{/each}</div>
							</div>
							{#if !tk.revoked}<Button size="sm" variant="ghost" class="text-destructive" onclick={() => revoke(tk.id, tk.name)}>{t('tokRevoke')}</Button>{/if}
						</li>
					{/each}
				</ul>
			{/if}
		</Loading>
	</Section>

	{#snippet aside()}
		<Section title={t('admCluster')} count={peers.data?.length}>
			<Loading state={peers}>
				<ul class="space-y-2 text-sm">
					<li class="flex items-center gap-2"><Dot tone="done" /><span class="font-medium">{session.machine}</span><span class="text-xs text-muted-foreground">{t('thisMachine')}</span></li>
					{#each peers.data || [] as p (p.name)}
						<li class="flex items-center gap-2">
							<Dot tone={p.connected ? 'done' : 'idle'} pulse={p.connected} /><span>{p.name}</span>
							<span class="text-xs text-muted-foreground">{p.connected ? t('admConnected') : t('admDisconnected')}</span>
							{#if p.connected}<Button size="sm" variant="ghost" class="ml-auto h-7" onclick={() => stopPeers([p.name])}><PowerIcon />{t('admStop')}</Button>{/if}
						</li>
					{/each}
				</ul>
				<div class="mt-4 flex flex-wrap gap-2 border-t pt-3">
					{#if (peers.data || []).some((p) => p.connected)}<Button size="sm" variant="outline" onclick={() => stopPeers([])}><PowerIcon />{t('admStopAll')}</Button>{/if}
					<Button size="sm" variant="outline" class="text-destructive" onclick={leave}><LogOutIcon />{t('admLeave')}</Button>
				</div>
				<p class="mt-2 text-[11px] text-muted-foreground">{t('admLeaveHint')}</p>
			</Loading>
		</Section>
		<Section title={t('admAbout')}>
			<dl class="num grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 text-xs">
				<dt class="text-muted-foreground">{t('versionLabel')}</dt><dd>{session.version}</dd>
				<dt class="text-muted-foreground">{t('admBuilt')}</dt><dd>{when(session.buildTime)} {session.buildTime ? '(' + ago(session.buildTime) + ')' : ''}</dd>
				{#if session.outdated}<dt class="text-muted-foreground">{t('admNewest')}</dt><dd class="text-st-needs">{session.newestVersion} @ {session.newestMachine}</dd>{/if}
			</dl>
		</Section>
	{/snippet}
</PageGrid>
