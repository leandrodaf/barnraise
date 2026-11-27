<script lang="ts">
	// Which MCP servers Barnraise's agents may use. The owner's own Claude,
	// opened in the project's folder, keeps every server; here each server
	// the machines have shows per machine (has it, answers, how many tools),
	// with a switch that lets agents use it and the roles that get it.
	import { Button } from '$lib/components/ui/button';
	import { Badge } from '$lib/components/ui/badge';
	import { Input } from '$lib/components/ui/input';
	import { Switch } from '$lib/components/ui/switch';
	import PlugIcon from '@lucide/svelte/icons/plug';
	import GlobeIcon from '@lucide/svelte/icons/globe';
	import TerminalIcon from '@lucide/svelte/icons/terminal';
	import RefreshIcon from '@lucide/svelte/icons/refresh-cw';
	import ChevronIcon from '@lucide/svelte/icons/chevron-down';
	import { toast } from 'svelte-sonner';
	import { api } from '$lib/api';
	import { t } from '$lib/i18n';
	import { ago } from '$lib/format';
	import { session } from '$lib/session.svelte';
	import Section from '$lib/app/Section.svelte';
	import Dot from '$lib/app/Dot.svelte';
	import type { AgentMCPResponse, AgentMCPServer, AgentMCPStatus } from '$lib/gen/barnraise/v1/barnraise_pb';

	let project = $state(session.projects.find((p) => p === 'one-more-bastion') || session.projects[0] || '');
	let data = $state<AgentMCPResponse | null>(null);
	let err = $state('');
	let busy = $state('');
	let open = $state<Record<string, boolean>>({});
	let hostDraft = $state<Record<string, string>>({});

	async function load() {
		if (!project) return;
		try {
			data = await api.listAgentMCP({ project });
			err = '';
		} catch (e) {
			err = e instanceof Error ? e.message : String(e);
		}
	}
	$effect(() => {
		project;
		data = null;
		load();
		const id = setInterval(load, 15000);
		return () => clearInterval(id);
	});

	async function save(s: AgentMCPServer, change: Partial<{ allowed: boolean; roles: string[]; hosts: string[] }>) {
		busy = 'save' + s.name;
		try {
			data = await api.setAgentMCP({ project, name: s.name, allowed: change.allowed ?? s.allowed, roles: change.roles ?? s.roles, hosts: change.hosts ?? s.hosts });
			if (change.allowed !== undefined) toast.success(change.allowed ? t('mcpAllowed', s.name) : t('mcpBlocked', s.name));
		} catch (e) {
			toast.error(e instanceof Error ? e.message : String(e));
		}
		busy = '';
	}
	function toggleRole(s: AgentMCPServer, role: string) {
		const roles = s.roles.includes(role) ? s.roles.filter((r) => r !== role) : [...s.roles, role];
		save(s, { roles });
	}
	function saveHosts(s: AgentMCPServer) {
		const hosts = (hostDraft[s.name] ?? s.hosts.join(', ')).split(/[\s,]+/).filter(Boolean);
		save(s, { hosts });
	}

	async function test(s: AgentMCPServer) {
		busy = 'test' + s.name;
		try {
			const r = await api.testAgentMCP({ project, name: s.name });
			if (data) {
				const byMachine = new Map(r.machines.map((m) => [m.machine, m]));
				s.machines = s.machines.map((m) => byMachine.get(m.machine) || m);
				for (const m of r.machines) if (!s.machines.some((x) => x.machine === m.machine)) s.machines.push(m);
			}
			open[s.name] = true;
			const bad = r.machines.filter((m) => m.found && !m.ok);
			const missing = r.machines.filter((m) => !m.found);
			if (!bad.length && !missing.length) toast.success(t('mcpTestOk', s.name, r.machines.length));
			else toast.warning(t('mcpTestSome', s.name, bad.length, missing.length));
		} catch (e) {
			toast.error(e instanceof Error ? e.message : String(e));
		}
		busy = '';
	}

	const tone = (m: AgentMCPStatus) => (!m.found ? 'idle' : m.ok ? 'done' : m.testedAt ? 'fail' : 'needs');
	const label = (m: AgentMCPStatus) =>
		!m.found ? t('mcpMissing') : m.ok ? t('mcpTools', m.tools.length) : m.testedAt ? t('mcpFailed') : t('mcpUntested');
	const sourceLabel = (src: string) => (src ? t('mcpSrc_' + src) : '');
	// Granted but missing on a machine: its agents run without it.
	const gap = (s: AgentMCPServer) => s.allowed && s.machines.some((m) => !m.found);
</script>

<Section title={t('mcpTitle')} sub={t('mcpHelp')} count={data?.servers.length}>
	{#snippet right()}
		<div class="flex items-center gap-2">
			{#if session.projects.length > 1}
				<select class="rounded-md border bg-background px-2 py-1 text-sm" bind:value={project}>
					{#each session.projects as p (p)}<option value={p}>{p}</option>{/each}
				</select>
			{/if}
			<button class="rounded p-1 hover:bg-muted" onclick={load} aria-label={t('refresh')}><RefreshIcon class="size-4" /></button>
		</div>
	{/snippet}
	{#if err}
		<p class="text-sm text-destructive">{err}</p>
	{:else if !data}
		<p class="text-sm text-muted-foreground">…</p>
	{:else if !data.servers.length}
		<p class="text-sm text-muted-foreground">{t('mcpNone')}</p>
	{:else}
		<ul class="-mx-4 divide-y">
			{#each data.servers as s (s.name)}
				<li class="px-4 py-3">
					<div class="flex flex-wrap items-start gap-3">
						<div class="mt-0.5 shrink-0 text-muted-foreground">{#if s.kind === 'remote'}<GlobeIcon class="size-4" />{:else if s.kind === 'local'}<TerminalIcon class="size-4" />{:else}<PlugIcon class="size-4" />{/if}</div>
						<div class="min-w-0 flex-1">
							<p class="flex flex-wrap items-center gap-2 text-sm font-medium">
								{s.name}
								{#if s.fromYaml}<Badge variant="outline">barnraise.yaml</Badge>{/if}
								{#if gap(s)}<Badge variant="secondary" class="text-st-needs">{t('mcpGap')}</Badge>{/if}
							</p>
							{#if s.summary}<p class="num truncate text-[11px] text-muted-foreground" title={s.summary}>{s.summary}</p>{/if}
							<div class="mt-1.5 flex flex-wrap gap-1.5">
								{#each s.machines as m (m.machine)}
									<button class="flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-[11px] hover:bg-muted" title={m.error || sourceLabel(m.source)} onclick={() => (open[s.name] = !open[s.name])}>
										<Dot tone={tone(m)} /><span class="font-medium">{m.machine}</span><span class="text-muted-foreground">{label(m)}</span>
									</button>
								{/each}
							</div>
						</div>
						<div class="flex items-center gap-2">
							<Button size="sm" variant="outline" disabled={busy === 'test' + s.name} onclick={() => test(s)}><RefreshIcon class={busy === 'test' + s.name ? 'animate-spin' : ''} />{busy === 'test' + s.name ? t('mcpTesting') : t('mcpTest')}</Button>
							<label class="flex items-center gap-2 text-xs" title={s.fromYaml ? t('mcpYamlHelp') : t('mcpSwitchHelp')}>
								<Switch checked={s.allowed} disabled={s.fromYaml || busy === 'save' + s.name} onCheckedChange={(v) => save(s, { allowed: v })} aria-label={t('mcpSwitch', s.name)} />
								<span class={s.allowed ? 'font-medium' : 'text-muted-foreground'}>{s.allowed ? t('mcpOn') : t('mcpOff')}</span>
							</label>
							<button class="rounded p-1 hover:bg-muted" onclick={() => (open[s.name] = !open[s.name])} aria-label={t('mcpDetails')}><ChevronIcon class="size-4 transition-transform {open[s.name] ? 'rotate-180' : ''}" /></button>
						</div>
					</div>
					{#if s.allowed && !s.fromYaml}
						<div class="mt-2 flex flex-wrap items-center gap-1.5 pl-7 text-xs">
							<span class="text-muted-foreground">{t('mcpRoles')}</span>
							{#each data.roles as r (r)}
								<button class="rounded-full border px-2 py-0.5 {s.roles.includes(r) ? 'border-primary bg-primary text-primary-foreground' : 'hover:bg-muted'}" disabled={busy === 'save' + s.name} onclick={() => toggleRole(s, r)}>{r}</button>
							{/each}
							<span class="text-muted-foreground">{s.roles.length ? '' : t('mcpAllRoles')}</span>
						</div>
					{/if}
					{#if open[s.name]}
						<div class="mt-3 space-y-2 pl-7 text-xs">
							{#each s.machines as m (m.machine)}
								<div>
									<p class="flex flex-wrap items-center gap-2"><Dot tone={tone(m)} /><span class="font-medium">{m.machine}</span>
										{#if m.found}<span class="text-muted-foreground">{sourceLabel(m.source)}</span>{/if}
										{#if m.testedAt}<span class="text-muted-foreground">· {t('mcpTestedAgo', ago(m.testedAt))}</span>{/if}
									</p>
									{#if !m.found}<p class="text-muted-foreground">{t('mcpMissingHelp')}</p>{/if}
									{#if m.error}<p class="break-words text-destructive">{m.error}</p>{/if}
									{#if m.tools.length}<div class="mt-1 flex flex-wrap gap-1">{#each m.tools as tool (tool)}<code class="num rounded bg-muted px-1 py-px text-[10px]">{tool}</code>{/each}</div>{/if}
								</div>
							{/each}
							{#if s.allowed && s.kind === 'local' && !s.fromYaml}
								<div class="pt-1">
									<p class="mb-1 text-muted-foreground">{t('mcpHostsHelp')}</p>
									<div class="flex gap-2">
										<Input class="h-8 text-xs" placeholder="api.example.com" value={hostDraft[s.name] ?? s.hosts.join(', ')} oninput={(e) => (hostDraft[s.name] = (e.target as HTMLInputElement).value)} />
										<Button size="sm" variant="outline" onclick={() => saveHosts(s)}>{t('save')}</Button>
									</div>
								</div>
							{/if}
						</div>
					{/if}
				</li>
			{/each}
		</ul>
	{/if}
</Section>
