<script lang="ts">
	// What runs on each machine: every feature of Barnraise, native or
	// optional, with its state per machine, why, what runs instead and the
	// fix. Tools Barnraise can install (pinned, checksum-verified, no root)
	// install here, on one machine, the ones picked or all of them.
	import { toast } from 'svelte-sonner';
	import * as Tooltip from '$lib/components/ui/tooltip';
	import { Badge } from '$lib/components/ui/badge';
	import { Button } from '$lib/components/ui/button';
	import { Spinner } from '$lib/components/ui/spinner';
	import BlocksIcon from '@lucide/svelte/icons/blocks';
	import RefreshIcon from '@lucide/svelte/icons/refresh-cw';
	import DownloadIcon from '@lucide/svelte/icons/download';
	import TrashIcon from '@lucide/svelte/icons/trash-2';
	import CopyIcon from '@lucide/svelte/icons/copy';
	import CheckIcon from '@lucide/svelte/icons/circle-check';
	import PowerIcon from '@lucide/svelte/icons/power';
	import LifeBuoyIcon from '@lucide/svelte/icons/life-buoy';
	import XIcon from '@lucide/svelte/icons/circle-x';
	import MinusIcon from '@lucide/svelte/icons/circle-minus';
	import ShieldIcon from '@lucide/svelte/icons/shield';
	import LogInIcon from '@lucide/svelte/icons/log-in';
	import ChevronRightIcon from '@lucide/svelte/icons/chevron-right';
	import * as Collapsible from '$lib/components/ui/collapsible';
	import { loginPanel } from '$lib/app/login.svelte';
	import { api, errorText } from '$lib/api';
	import { getLang } from '$lib/i18n';
	import { ago } from '$lib/format';
	import { session } from '$lib/session.svelte';
	import PageHeader from '$lib/app/PageHeader.svelte';
	import Empty from '$lib/app/Empty.svelte';
	import type { CapabilityItem, MachineCapabilities, ToolInstallResult } from '$lib/gen/barnraise/v1/barnraise_pb';
	import { SOFT, TEXT, type Tone } from '$lib/status';

	// The page's own words; the shared dictionary stays as it is.
	const EN: Record<string, string> = {
		title: 'Capabilities', help: 'What runs on each machine, what falls back and why, and one click to install what is missing.',
		machines: '{0} machines', active: '{0} active', attention: '{0} need attention', refresh: 'Probe again',
		installMissing: 'Install what is missing everywhere', installMissingHere: 'Install missing',
		onlyAttention: 'Only what needs attention', everything: 'Everything', tools: 'Tools Barnraise installs',
		toolsHelp: 'Pinned versions, checksum-verified downloads from the official releases, installed without root in Barnraise’s own folder.',
		install: 'Install', update: 'Update to {0}', remove: 'Remove', onAll: 'On every machine', notShipped: 'no build for this platform',
		fallback: 'Instead', fix: 'Fix', root: 'needs root: run it yourself', copied: 'Copied', unreachable: 'Not reachable',
		probed: 'probed {0}', installed: '{0} installed on {1}', removed: '{0} removed from {1}', failed: '{0} on {1}: {2}',
		installing: 'Installing {0}…', nothing: 'Nothing is missing that Barnraise can install.', allGood: 'Everything works on every machine.',
		st_active: 'Active', st_available: 'Available, off', st_degraded: 'On a fallback', st_missing: 'Missing', st_unsupported: 'Not for this OS',
		k_native: 'native', k_optional: 'optional', k_plugin: 'plugin', version: 'version {0}', pinned: 'pinned {0}', managed: 'by Barnraise',
		g_security: 'Security', g_isolation: 'Isolation', g_merge: 'Merge', g_checks: 'Checks', g_context: 'Context and search',
		g_tokens: 'Tokens', g_worktrees: 'Worktrees', g_providers: 'Agents and providers', g_telemetry: 'Telemetry', g_cluster: 'Machines network', g_plugins: 'Plugins', g_other: 'Other',
		todoTitle: 'What needs you', todoNone: 'Everything that is switched on works on every machine.', todoOn: 'On {0}', todoNow: 'Meanwhile: {0}',
		doInstall: 'Install on {0}', doLogin: 'Log in to the account', doRun: 'Run this once in a terminal of {0}:', doRunRoot: 'It needs your password (sudo): Barnraise never runs it for you.',
		doCopy: 'Copy the command', doConfig: 'Change this in the configuration:', working: 'Working ({0})', optional: 'Optional, switched off ({0})',
		optionalHelp: 'None of these is a problem: they are extras you can switch on when you want them.', details: 'Each machine, item by item', turnOn: 'To switch it on'
	};
	const PT: Record<string, string> = {
		title: 'Capacidades', help: 'O que roda em cada máquina, o que cai num plano B e por quê, e um clique para instalar o que falta.',
		machines: '{0} máquinas', active: '{0} ativas', attention: '{0} pedem atenção', refresh: 'Verificar de novo',
		installMissing: 'Instalar o que falta em todas', installMissingHere: 'Instalar o que falta',
		onlyAttention: 'Só o que pede atenção', everything: 'Tudo', tools: 'Ferramentas que o Barnraise instala',
		toolsHelp: 'Versões fixas, download verificado por checksum das releases oficiais, instaladas sem root na pasta do próprio Barnraise.',
		install: 'Instalar', update: 'Atualizar para {0}', remove: 'Remover', onAll: 'Em todas as máquinas', notShipped: 'sem build para esta plataforma',
		fallback: 'No lugar', fix: 'Como resolver', root: 'precisa de root: rode você mesmo', copied: 'Copiado', unreachable: 'Fora de alcance',
		probed: 'verificada {0}', installed: '{0} instalado em {1}', removed: '{0} removido de {1}', failed: '{0} em {1}: {2}',
		installing: 'Instalando {0}…', nothing: 'Não falta nada que o Barnraise possa instalar.', allGood: 'Tudo funciona em todas as máquinas.',
		st_active: 'Ativo', st_available: 'Disponível, desligado', st_degraded: 'No plano B', st_missing: 'Faltando', st_unsupported: 'Não é para este SO',
		k_native: 'nativo', k_optional: 'opcional', k_plugin: 'plugin', version: 'versão {0}', pinned: 'fixada {0}', managed: 'pelo Barnraise',
		g_security: 'Segurança', g_isolation: 'Isolamento', g_merge: 'Merge', g_checks: 'Checks', g_context: 'Contexto e busca',
		g_tokens: 'Tokens', g_worktrees: 'Worktrees', g_providers: 'Agentes e provedores', g_telemetry: 'Telemetria', g_cluster: 'Rede entre máquinas', g_plugins: 'Plugins', g_other: 'Outros',
		todoTitle: 'O que precisa de você', todoNone: 'Tudo que está ligado funciona em todas as máquinas.', todoOn: 'Em {0}', todoNow: 'Enquanto isso: {0}',
		doInstall: 'Instalar em {0}', doLogin: 'Entrar com a conta', doRun: 'Rode isto uma vez num terminal da {0}:', doRunRoot: 'Pede a sua senha (sudo): o Barnraise nunca roda isso por você.',
		doCopy: 'Copiar o comando', doConfig: 'Mude isto na configuração:', working: 'Funcionando ({0})', optional: 'Opcionais desligados ({0})',
		optionalHelp: 'Nada disto é problema: são extras que você liga quando quiser.', details: 'Cada máquina, item por item', turnOn: 'Para ligar'
	};
	// What each capability is, in plain words, for who does not know the name.
	const ABOUT_PT: Record<string, string> = {
		'security.gitleaks': 'Procura senhas e chaves esquecidas no código antes de integrar.',
		'security.trust': 'Cada agente só usa o próprio acesso, e o histórico não pode ser adulterado.',
		'egress.proxy': 'Deixa os agentes acessarem só os sites liberados na internet.',
		'egress.rules': 'Na internet, os agentes só escrevem no repositório do projeto.',
		'egress.broker': 'As chaves dos provedores ficam fora do alcance dos agentes.',
		'egress.inspect': 'Confere o que os agentes enviam para fora e guarda um recibo assinado.',
		'sandbox.bubblewrap': 'Caixa de areia que prende o Codex e os terminais dos agentes.',
		'sandbox.landlock': 'O próprio Linux impede os agentes de mexer fora da pasta do item.',
		'sandbox.microvm': 'Roda cada agente numa máquina virtual pequena e separada.',
		'sandbox.profiles': 'Regras do que cada etapa (preparo, verificação, sessão) pode tocar.',
		'merge.rerere': 'Lembra como um conflito foi resolvido e repete sozinho.',
		'merge.structural': 'Junta mudanças entendendo o código, com menos conflitos.',
		'checks.ast-grep': 'Verificação de padrões de código pela estrutura.',
		'checks.dependencies': 'Avisa quando uma dependência tem falha de segurança conhecida.',
		'context.lessons': 'O que deu errado numa entrega vira lição para as próximas.',
		'context.map': 'Um mapa do repositório para o agente se achar rápido.',
		'context.search': 'Busca rápida no código.',
		'context.stale-reads': 'Avisa o agente quando um arquivo que ele leu mudou na main.',
		'context.symbols': 'Encontra funções e tipos pela estrutura do código.',
		'tokens.outfilter': 'Corta saídas longas de comandos para gastar menos tokens.',
		'tokens.rtk': 'Comprime ainda mais a saída dos comandos.',
		'worktrees.build-cache': 'Reaproveita o build entre itens para não compilar tudo de novo.',
		'worktrees.containers': 'Roda os agentes dentro de contêineres.',
		'worktrees.git': 'Cada item trabalha numa cópia separada do repositório.',
		'worktrees.hooks': 'Comandos seus que rodam ao criar ou apagar a cópia de um item.',
		'worktrees.services': 'Sobe banco e serviços separados para cada item.',
		'providers.claude': 'Os agentes que usam o Claude.',
		'providers.codex': 'Os agentes que usam o Codex (OpenAI).',
		'providers.gemini': 'Os agentes que usam o Gemini (Google).',
		'telemetry.otel': 'Manda métricas e rastros para um painel externo (Grafana etc.).',
		'cluster.network': 'A rede que liga as suas máquinas.'
	};
	const w = (k: string, ...a: unknown[]) =>
		((getLang() === 'pt-BR' ? PT : EN)[k] ?? EN[k] ?? k).replace(/\{(\d)\}/g, (_, i) => String(a[Number(i)] ?? ''));

	const GROUPS = ['security', 'isolation', 'merge', 'checks', 'context', 'tokens', 'worktrees', 'providers', 'telemetry', 'cluster', 'plugins'];
	const STATUS: Record<string, { tone: Tone; icon: typeof CheckIcon; rank: number }> = {
		active: { tone: 'done', icon: CheckIcon, rank: 0 },
		available: { tone: 'idle', icon: PowerIcon, rank: 1 },
		degraded: { tone: 'needs', icon: LifeBuoyIcon, rank: 2 },
		missing: { tone: 'fail', icon: XIcon, rank: 3 },
		unsupported: { tone: 'idle', icon: MinusIcon, rank: 1 }
	};
	const st = (s: string) => STATUS[s] || STATUS.unsupported;
	const needsAttention = (s: string) => s === 'degraded' || s === 'missing';

	let report = $state<MachineCapabilities[] | null>(null);
	let error = $state('');
	let loading = $state(true);
	let onlyAttention = $state(false);
	let busy = $state<Record<string, boolean>>({}); // "machine/tool" or "*/tool" or "*"

	async function read(refresh = false) {
		loading = true;
		try {
			const r = await api.capabilities({ all: true, refresh, project: session.scope || '' });
			report = [...r.machines].sort((a, b) => (a.machine === session.machine ? -1 : b.machine === session.machine ? 1 : a.machine.localeCompare(b.machine)));
			error = '';
		} catch (e) {
			if (!session.failed(e)) error = errorText(e);
		} finally {
			loading = false;
		}
	}
	$effect(() => {
		void session.scope;
		read();
	});

	const machines = $derived(report || []);
	// One row per capability, with each machine's cell; ordered by group, then
	// by how much the row needs the owner.
	const rows = $derived.by(() => {
		const by = new Map<string, { item: CapabilityItem; cells: Record<string, CapabilityItem> }>();
		for (const m of machines)
			for (const it of m.items) {
				const r = by.get(it.id) || { item: it, cells: {} };
				r.cells[m.machine] = it;
				by.set(it.id, r);
			}
		const worst = (r: { cells: Record<string, CapabilityItem> }) => Math.max(...Object.values(r.cells).map((c) => st(c.status).rank));
		const list = [...by.values()].filter((r) => !onlyAttention || Object.values(r.cells).some((c) => c.on && needsAttention(c.status)));
		const groups = new Map<string, typeof list>();
		for (const r of list) {
			const g = GROUPS.includes(r.item.group) ? r.item.group : 'other';
			groups.set(g, [...(groups.get(g) || []), r]);
		}
		return [...GROUPS, 'other']
			.filter((g) => groups.has(g))
			.map((g) => ({ group: g, rows: groups.get(g)!.sort((a, b) => worst(b) - worst(a) || a.item.title.localeCompare(b.item.title)) }));
	});
	const total = $derived(machines.reduce((n, m) => n + m.items.filter((i) => i.status === 'active').length, 0));
	const attention = $derived(machines.reduce((n, m) => n + m.items.filter((i) => i.on && needsAttention(i.status)).length, 0));
	// Tools any machine misses and Barnraise can install there.
	const missingTools = (m: MachineCapabilities) =>
		[...new Set(m.items.filter((i) => needsAttention(i.status) && i.tool && !i.needsRoot).map((i) => i.tool))].filter((id) => m.tools.find((t) => t.id === id)?.available !== false);
	const anyMissing = $derived(machines.some((m) => missingTools(m).length));
	const toolIds = $derived([...new Set(machines.flatMap((m) => m.tools.map((t) => t.id)))].sort());
	const toolTitle = (id: string) => machines.flatMap((m) => m.tools).find((t) => t.id === id)?.title || id;

	function report1(results: ToolInstallResult[], remove: boolean) {
		for (const r of results) {
			if (r.ok) toast.success(w(remove ? 'removed' : 'installed', `${toolTitle(r.tool)}${r.version ? ' ' + r.version : ''}`, r.machine));
			else toast.error(w('failed', toolTitle(r.tool), r.machine, r.error));
		}
	}
	async function install(tools: string[], machinesPicked: string[], all: boolean, key: string, remove = false) {
		if (!tools.length || busy[key]) return;
		busy[key] = true;
		const tid = toast.loading(w('installing', tools.map(toolTitle).join(', ')));
		try {
			const r = await api.installTools({ tools, machines: machinesPicked, all, remove });
			toast.dismiss(tid);
			report1(r.results, remove);
			await read(true);
		} catch (e) {
			toast.dismiss(tid);
			if (!session.failed(e)) toast.error(errorText(e));
		} finally {
			busy[key] = false;
		}
	}
	async function installEverywhere() {
		const todo = machines.map((m) => ({ m: m.machine, tools: missingTools(m) })).filter((x) => x.tools.length);
		busy['*'] = true;
		try {
			await Promise.all(todo.map((x) => install(x.tools, [x.m], false, x.m + '/*')));
		} finally {
			busy['*'] = false;
		}
	}
	async function copy(text: string) {
		try {
			await navigator.clipboard.writeText(text);
			toast.success(w('copied'));
		} catch {
			/* no clipboard: the text stays selectable */
		}
	}
	const segments = (m: MachineCapabilities) => {
		const n = m.items.length || 1;
		return ['active', 'available', 'degraded', 'missing', 'unsupported']
			.map((s) => ({ s, n: m.items.filter((i) => i.status === s).length }))
			.filter((x) => x.n)
			.map((x) => ({ ...x, pct: (x.n * 100) / n }));
	};
	const about = (id: string, title: string) => (getLang() === 'pt-BR' ? ABOUT_PT[id] || title : title);
	// What needs the owner: switched on (the daemon's word) and not running as
	// designed, one entry per capability with the machines it touches.
	type Todo = { item: CapabilityItem; cells: { m: string; c: CapabilityItem }[] };
	const todos = $derived.by(() => {
		const by = new Map<string, Todo>();
		for (const m of machines)
			for (const c of m.items)
				if (c.on && needsAttention(c.status)) {
					const t = by.get(c.id) || { item: c, cells: [] };
					t.cells.push({ m: m.machine, c });
					by.set(c.id, t);
				}
		return [...by.values()].sort((a, b) => st(b.item.status).rank - st(a.item.status).rank);
	});
	const isLogin = (c: CapabilityItem) => /^barnraise account add (claude|codex)$/.test(c.fix);
	const cmdOf = (fix: string) => {
		const m = fix.match(/(?:^|: )((?:sudo |npm |printf |barnraise ).*)$/);
		return m ? m[1].replace(/\s+\(.*\)$/, '') : '';
	};
	const isConfig = (fix: string) => /^(barnraise\.yaml|daemon\.yaml):/.test(fix);
	const working = $derived(rows.flatMap((g) => g.rows).filter((r) => Object.values(r.cells).every((c) => c.status === 'active')));
	const optional = $derived(rows.flatMap((g) => g.rows).filter((r) => Object.values(r.cells).some((c) => !c.on && c.status !== 'active' && c.status !== 'unsupported')));
	let showDetails = $state(false);

	const BAR: Record<string, string> = { active: 'bg-st-done', available: 'bg-st-idle/50', degraded: 'bg-st-needs', missing: 'bg-st-fail', unsupported: 'bg-st-idle/25' };
</script>

<PageHeader
	title={w('title')}
	icon={BlocksIcon}
	subtitle={w('help')}
	meta={[w('machines', machines.length), w('active', total), attention ? w('attention', attention) : false]}
>
	{#snippet actions()}
		<div class="flex rounded-md border p-0.5 text-xs">
			<button class="rounded px-2 py-1 {onlyAttention ? 'text-muted-foreground' : 'bg-muted font-medium'}" onclick={() => (onlyAttention = false)}>{w('everything')}</button>
			<button class="rounded px-2 py-1 {onlyAttention ? 'bg-muted font-medium' : 'text-muted-foreground'}" onclick={() => (onlyAttention = true)}>{w('onlyAttention')}</button>
		</div>
		<Button variant="outline" size="sm" onclick={() => read(true)} disabled={loading}>
			{#if loading}<Spinner class="size-3.5" />{:else}<RefreshIcon class="size-3.5" />{/if}{w('refresh')}
		</Button>
		{#if anyMissing}
			<Button size="sm" onclick={installEverywhere} disabled={busy['*']}>
				{#if busy['*']}<Spinner class="size-3.5" />{:else}<DownloadIcon class="size-3.5" />{/if}{w('installMissing')}
			</Button>
		{/if}
	{/snippet}
</PageHeader>

{#if error}<p class="mb-4 rounded-lg border border-st-fail/40 bg-st-fail/10 px-3 py-2 text-sm text-st-fail">{error}</p>{/if}
{#if !report}<Empty text="…" />{:else if !machines.length}<Empty />{/if}

{#if machines.length}
	<!-- What needs the owner, in plain words, with the one thing to do. -->
	<section class="mb-6 space-y-3">
		<h2 class="text-sm font-semibold">{w('todoTitle')}</h2>
		{#if !todos.length}
			<p class="flex items-center gap-2 rounded-xl border bg-card px-4 py-3 text-sm"><CheckIcon class="size-4 {TEXT.done}" />{w('todoNone')}</p>
		{/if}
		{#each todos as td (td.item.id)}
			{@const s0 = st(td.item.status)}
			{@const names = td.cells.map((x) => x.m)}
			{@const tools = td.cells.filter((x) => x.c.tool && !x.c.needsRoot)}
			<article class="space-y-3 rounded-xl border bg-card p-4 {td.item.status === 'missing' ? 'border-st-fail/40' : 'border-st-needs/40'}">
				<div class="flex flex-wrap items-start gap-3">
					<s0.icon class="mt-0.5 size-4 shrink-0 {TEXT[s0.tone]}" />
					<div class="min-w-0 flex-1 space-y-1">
						<p class="font-medium">{about(td.item.id, td.item.title)}</p>
						<p class="num text-[11px] text-muted-foreground">{td.item.title} · {w('todoOn', names.join(', '))}</p>
						{#each td.cells as x (x.m)}<p class="text-sm text-muted-foreground">{names.length > 1 ? x.m + ': ' : ''}{x.c.reason}</p>{/each}
						{#if td.item.fallback}<p class="text-xs text-muted-foreground">{w('todoNow', td.item.fallback)}</p>{/if}
					</div>
				</div>
				<div class="space-y-2 sm:pl-7">
					{#if tools.length}
						<Button size="sm" disabled={busy['todo/' + td.item.id]} onclick={() => Promise.all(tools.map((x) => install([x.c.tool], [x.m], false, 'todo/' + td.item.id)))}>
							{#if busy['todo/' + td.item.id]}<Spinner class="size-3.5" />{:else}<DownloadIcon class="size-3.5" />{/if}{w('doInstall', tools.map((x) => x.m).join(', '))}
						</Button>
					{:else if isLogin(td.item)}
						<Button size="sm" onclick={() => loginPanel.show('', td.item.fix.split(' ').pop())}><LogInIcon class="size-3.5" />{w('doLogin')}</Button>
					{:else}
						{#each td.cells as x (x.m)}
							{@const cmd = cmdOf(x.c.fix)}
							{#if cmd}
								<div class="space-y-1.5">
									<p class="text-sm">{w('doRun', x.m)}</p>
									<div class="flex items-start gap-2">
										<code class="num min-w-0 flex-1 rounded-md bg-muted px-3 py-2 text-[11px] break-all">{cmd}</code>
										<Button size="sm" variant="outline" onclick={() => copy(cmd)}><CopyIcon class="size-3.5" />{w('doCopy')}</Button>
									</div>
									{#if x.c.needsRoot}<p class="flex items-center gap-1 text-xs text-muted-foreground"><ShieldIcon class="size-3" />{w('doRunRoot')}</p>{/if}
								</div>
							{:else if x.c.fix}
								<p class="text-sm">{isConfig(x.c.fix) ? w('doConfig') : ''} <code class="num rounded bg-muted px-1.5 py-0.5 text-[11px] break-all">{x.c.fix}</code></p>
							{/if}
						{/each}
					{/if}
				</div>
			</article>
		{/each}
	</section>

	<!-- One card per machine: the share of each state and the one-click fix. -->
	<div class="mb-6 grid gap-3 sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4">
		{#each machines as m (m.machine)}
			{@const miss = missingTools(m)}
			{@const att = m.items.filter((i) => i.on && needsAttention(i.status)).length}
			<article class="space-y-2.5 rounded-xl border bg-card p-3.5 {m.error ? 'border-dashed opacity-70' : ''}">
				<div class="flex items-start justify-between gap-2">
					<div class="min-w-0">
						<p class="flex items-center gap-2 font-semibold"><span class="truncate">{m.machine}</span>{#if m.machine === session.machine}<Badge variant="outline" class="h-4 px-1.5 text-[10px]">●</Badge>{/if}</p>
						<p class="num text-[11px] text-muted-foreground">{m.platform}{m.probedAt ? ' · ' + w('probed', ago(m.probedAt)) : ''}</p>
					</div>
					{#if m.error}<span class="text-xs text-st-fail">{w('unreachable')}</span>
					{:else if att}<span class="num rounded-md px-1.5 py-0.5 text-xs {SOFT.needs}">{att}</span>
					{:else}<CheckIcon class="size-4 {TEXT.done}" />{/if}
				</div>
				{#if m.error}<p class="text-xs text-muted-foreground">{m.error}</p>
				{:else}
					<div class="flex h-2 overflow-hidden rounded-full bg-muted" role="img" aria-label={segments(m).map((x) => `${w('st_' + x.s)} ${x.n}`).join(', ')}>
						{#each segments(m) as x (x.s)}<div class="{BAR[x.s]} h-full" style="width:{x.pct}%"></div>{/each}
					</div>
					<div class="num flex flex-wrap gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
						{#each segments(m) as x (x.s)}<span class="flex items-center gap-1"><span class="size-1.5 rounded-full {BAR[x.s]}"></span>{w('st_' + x.s)} {x.n}</span>{/each}
					</div>
					{#if miss.length}
						<Button size="sm" variant="secondary" class="w-full min-w-0" title={miss.map(toolTitle).join(', ')} disabled={busy[m.machine + '/*']} onclick={() => install(miss, [m.machine], false, m.machine + '/*')}>
							{#if busy[m.machine + '/*']}<Spinner class="size-3.5" />{:else}<DownloadIcon class="size-3.5" />{/if}<span class="truncate">{w('installMissingHere')} ({miss.length})</span>
						</Button>
						<p class="truncate text-[11px] text-muted-foreground">{miss.map(toolTitle).join(' · ')}</p>
					{/if}
				{/if}
			</article>
		{/each}
	</div>

	<!-- Extras switched off: not problems, said once, with how to switch them on. -->
	{#if optional.length}
		<Collapsible.Root class="mb-4">
			<Collapsible.Trigger class="group flex w-full items-center gap-2 rounded-xl border bg-card px-4 py-3 text-sm font-medium hover:bg-muted">
				<PowerIcon class="size-4 text-muted-foreground" />{w('optional', optional.length)}<ChevronRightIcon class="ml-auto size-4 transition-transform group-data-[state=open]:rotate-90" />
			</Collapsible.Trigger>
			<Collapsible.Content>
				<div class="mt-2 divide-y rounded-xl border bg-card">
					<p class="px-4 py-2.5 text-xs text-muted-foreground">{w('optionalHelp')}</p>
					{#each optional as r (r.item.id)}
						{@const fix = Object.values(r.cells).find((c) => c.fix)?.fix || ''}
						<div class="space-y-1 px-4 py-2.5">
							<p class="text-sm">{about(r.item.id, r.item.title)}</p>
							<p class="num text-[11px] text-muted-foreground">{r.item.title}{fix ? ' · ' + w('turnOn') + ': ' : ''}{#if fix}<code class="break-all">{fix}</code>{/if}</p>
						</div>
					{/each}
				</div>
			</Collapsible.Content>
		</Collapsible.Root>
	{/if}

	<Collapsible.Root bind:open={showDetails} class="mb-4">
		<Collapsible.Trigger class="group flex w-full items-center gap-2 rounded-xl border bg-card px-4 py-3 text-sm font-medium hover:bg-muted">
			<BlocksIcon class="size-4 text-muted-foreground" />{w('details')}<span class="num text-xs font-normal text-muted-foreground">{w('working', working.length)}</span><ChevronRightIcon class="ml-auto size-4 transition-transform group-data-[state=open]:rotate-90" />
		</Collapsible.Trigger>
		<Collapsible.Content class="mt-3">
	<!-- The matrix: a row per capability, a column per machine. -->
	{#if !rows.length}
		<p class="mb-6 rounded-xl border bg-card px-4 py-6 text-center text-sm text-muted-foreground"><CheckIcon class="mx-auto mb-2 size-6 {TEXT.done}" />{w('allGood')}</p>
	{/if}
	<Tooltip.Provider delayDuration={150}>
		<div class="space-y-5">
			{#each rows as g (g.group)}
				<section class="overflow-hidden rounded-xl border bg-card">
					<h2 class="eyebrow border-b bg-muted/40 px-4 py-2 text-muted-foreground">{w('g_' + g.group)}</h2>
					<div class="overflow-x-auto">
						<table class="w-full text-sm">
							<thead>
								<tr class="text-left text-[11px] text-muted-foreground">
									<th class="w-[34%] min-w-56 px-4 py-2 font-medium"></th>
									{#each machines as m (m.machine)}<th class="min-w-36 px-3 py-2 font-medium"><span class="block max-w-40 truncate">{m.machine}</span></th>{/each}
								</tr>
							</thead>
							<tbody>
								{#each g.rows as r (r.item.id)}
									<tr class="border-t align-top">
										<td class="px-4 py-2.5">
											<p class="font-medium">{r.item.title}</p>
											<p class="num text-[11px] text-muted-foreground">{w('k_' + r.item.kind)} · {r.item.id}</p>
										</td>
										{#each machines as m (m.machine)}
											{@const c = r.cells[m.machine]}
											<td class="px-3 py-2.5">
												{#if c}
													{@const s = st(c.status)}
													<Tooltip.Root>
														<Tooltip.Trigger class="inline-flex max-w-full items-center gap-1.5 rounded-md px-2 py-1 text-xs font-medium {SOFT[s.tone]}">
															<s.icon class="size-3.5 shrink-0" /><span class="truncate">{w('st_' + c.status)}</span>
														</Tooltip.Trigger>
														<Tooltip.Content class="max-w-sm space-y-1.5 p-3 text-xs">
															<p class="font-semibold">{c.title} · {m.machine}</p>
															{#if c.reason}<p>{c.reason}</p>{/if}
															{#if c.version}<p class="num opacity-80">{w('version', c.version)}</p>{/if}
															{#if c.fallback}<p><span class="font-semibold">{w('fallback')}:</span> {c.fallback}</p>{/if}
															{#if c.fix}<p><span class="font-semibold">{w('fix')}:</span> <code class="num break-all">{c.fix}</code></p>{/if}
															{#if c.needsRoot}<p class="flex items-center gap-1 opacity-80"><ShieldIcon class="size-3" />{w('root')}</p>{/if}
														</Tooltip.Content>
													</Tooltip.Root>
													{#if needsAttention(c.status)}
														<div class="mt-1 flex flex-wrap gap-1">
															{#if c.tool && !c.needsRoot}
																<button class="inline-flex items-center gap-1 rounded border px-1.5 py-0.5 text-[11px] hover:bg-muted disabled:opacity-50" disabled={busy[m.machine + '/' + c.tool]} onclick={() => install([c.tool], [m.machine], false, m.machine + '/' + c.tool)}>
																	{#if busy[m.machine + '/' + c.tool]}<Spinner class="size-3" />{:else}<DownloadIcon class="size-3" />{/if}{w('install')}
																</button>
															{:else if c.fix}
																<button class="inline-flex items-center gap-1 rounded border px-1.5 py-0.5 text-[11px] hover:bg-muted" onclick={() => copy(c.fix)}><CopyIcon class="size-3" />{w('fix')}</button>
															{/if}
														</div>
													{/if}
												{:else}<span class="text-xs text-muted-foreground">—</span>{/if}
											</td>
										{/each}
									</tr>
								{/each}
							</tbody>
						</table>
					</div>
				</section>
			{/each}
		</div>
	</Tooltip.Provider>

		</Collapsible.Content>
	</Collapsible.Root>

	<!-- Tools: what each machine has, and install, update or remove on one or all. -->
	{#if toolIds.length}
		<section class="mt-6 overflow-hidden rounded-xl border bg-card">
			<div class="border-b px-4 py-3">
				<h2 class="font-semibold">{w('tools')}</h2>
				<p class="text-xs text-muted-foreground">{w('toolsHelp')}</p>
			</div>
			<div class="overflow-x-auto">
				<table class="w-full text-sm">
					<thead>
						<tr class="text-left text-[11px] text-muted-foreground">
							<th class="min-w-48 px-4 py-2 font-medium"></th>
							{#each machines as m (m.machine)}<th class="min-w-40 px-3 py-2 font-medium"><span class="block max-w-40 truncate">{m.machine}</span></th>{/each}
							<th class="px-3 py-2"></th>
						</tr>
					</thead>
					<tbody>
						{#each toolIds as id (id)}
							{@const pinned = machines.flatMap((m) => m.tools).find((t) => t.id === id)?.pinned || ''}
							<tr class="border-t align-top">
								<td class="px-4 py-2.5"><p class="font-medium">{toolTitle(id)}</p>{#if pinned}<p class="num text-[11px] text-muted-foreground">{w('pinned', pinned)}</p>{/if}</td>
								{#each machines as m (m.machine)}
									{@const tl = m.tools.find((x) => x.id === id)}
									{@const k = m.machine + '/' + id}
									<td class="px-3 py-2.5">
										{#if !tl || m.error}<span class="text-xs text-muted-foreground">—</span>
										{:else if !tl.available && !tl.installed}<span class="text-[11px] text-muted-foreground">{w('notShipped')}</span>
										{:else if tl.installed}
											<p class="num flex items-center gap-1 text-xs"><CheckIcon class="size-3.5 {TEXT.done}" />{tl.installed}{#if tl.managed}<span class="text-muted-foreground">· {w('managed')}</span>{/if}</p>
											<div class="mt-1 flex flex-wrap gap-1">
												{#if tl.managed && tl.pinned && tl.installed !== tl.pinned}
													<button class="inline-flex items-center gap-1 rounded border px-1.5 py-0.5 text-[11px] hover:bg-muted disabled:opacity-50" disabled={busy[k]} onclick={() => install([id], [m.machine], false, k)}>{#if busy[k]}<Spinner class="size-3" />{:else}<DownloadIcon class="size-3" />{/if}{w('update', tl.pinned)}</button>
												{/if}
												{#if tl.managed}
													<button class="inline-flex items-center gap-1 rounded border px-1.5 py-0.5 text-[11px] text-muted-foreground hover:bg-muted disabled:opacity-50" disabled={busy[k]} onclick={() => install([id], [m.machine], false, k, true)}><TrashIcon class="size-3" />{w('remove')}</button>
												{/if}
											</div>
										{:else}
											<button class="inline-flex items-center gap-1 rounded-md border px-2 py-1 text-xs hover:bg-muted disabled:opacity-50" disabled={busy[k]} onclick={() => install([id], [m.machine], false, k)}>{#if busy[k]}<Spinner class="size-3" />{:else}<DownloadIcon class="size-3" />{/if}{w('install')}</button>
										{/if}
									</td>
								{/each}
								<td class="px-3 py-2.5 text-right">
									{#if machines.some((m) => !m.error && m.tools.find((x) => x.id === id && x.available && !x.installed))}
										<Button size="sm" variant="outline" disabled={busy['*/' + id]} onclick={() => install([id], [], true, '*/' + id)}>
											{#if busy['*/' + id]}<Spinner class="size-3.5" />{:else}<DownloadIcon class="size-3.5" />{/if}{w('onAll')}
										</Button>
									{/if}
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
			{#if !anyMissing}<p class="border-t px-4 py-2 text-xs text-muted-foreground">{w('nothing')}</p>{/if}
		</section>
	{/if}
{/if}
