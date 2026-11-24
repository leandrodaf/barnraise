<script lang="ts">
	import * as Sidebar from '$lib/components/ui/sidebar';
	import * as Breadcrumb from '$lib/components/ui/breadcrumb';
	import { Separator } from '$lib/components/ui/separator';
	import { Toaster } from '$lib/components/ui/sonner';
	import { Spinner } from '$lib/components/ui/spinner';
	import MoonStarIcon from '@lucide/svelte/icons/moon-star';
	import PauseIcon from '@lucide/svelte/icons/circle-pause';
	import SearchIcon from '@lucide/svelte/icons/search';
	import { t } from '$lib/i18n';
	import { session } from '$lib/session.svelte';
	import { cluster } from '$lib/cluster.svelte';
	import { router, href } from '$lib/router.svelte';
	import '$lib/theme.svelte';
	import { NAV } from '$lib/app/nav';
	import { keys } from '$lib/app/keys.svelte';
	import { standby, resumeProject } from '$lib/app/actions';
	import FormDialog from '$lib/app/FormDialog.svelte';
	import AppSidebar from '$lib/app/AppSidebar.svelte';
	import StatusBar from '$lib/app/StatusBar.svelte';
	import CommandPalette from '$lib/app/CommandPalette.svelte';
	import ShortcutsDialog from '$lib/app/ShortcutsDialog.svelte';
	import DiscussionSheet from '$lib/app/DiscussionSheet.svelte';
	import LoginSheet from '$lib/app/LoginSheet.svelte';
	import ScopeSelect from '$lib/app/ScopeSelect.svelte';
	import VersionNotice from '$lib/app/VersionNotice.svelte';
	import AutopilotSwitch from '$lib/app/AutopilotSwitch.svelte';
	import SignIn from './pages/SignIn.svelte';
	import Home from './pages/Home.svelte';
	import Overview from './pages/Overview.svelte';
	import Queue from './pages/Queue.svelte';
	import Item from './pages/Item.svelte';
	import Agents from './pages/Agents.svelte';
	import Accounts from './pages/Accounts.svelte';
	import Proposals from './pages/Proposals.svelte';
	import Machines from './pages/Machines.svelte';
	import Metrics from './pages/Metrics.svelte';
	import Capabilities from './pages/Capabilities.svelte';
	import Parts from './pages/Parts.svelte';

	session.start().catch(() => (session.booting = false));

	let sidebarOpen = $state(window.innerWidth >= 1100);
	const here = $derived(NAV.find((n) => n.name === (router.current.name === 'item' ? 'queue' : router.current.name)));
	const paused = $derived((cluster.data?.projects || []).filter((p) => p.paused));
	// An item route: #/item/<project>/<id>; an old link without the project uses the scope's.
	const itemProject = $derived(router.current.args.length > 1 ? router.current.args[0] : session.project);
	const itemId = $derived(router.current.args.length > 1 ? router.current.args[1] : router.current.args[0] || '');
</script>

<Toaster richColors position="bottom-right" />

{#if session.booting}
	<div class="grid h-svh place-items-center"><Spinner class="size-6" /></div>
{:else if !session.signedIn}
	<SignIn />
{:else}
	<Sidebar.Provider class="h-svh overflow-hidden" open={sidebarOpen} onOpenChange={(v) => (sidebarOpen = v)}>
		<AppSidebar />
		<Sidebar.Inset class="flex h-svh min-w-0 flex-col overflow-hidden">
			<header class="flex h-12 shrink-0 items-center gap-1.5 border-b bg-background px-2 sm:px-3">
				<Sidebar.Trigger />
				<Separator orientation="vertical" class="mx-1 data-[orientation=vertical]:h-4" />
				<ScopeSelect />
				<Breadcrumb.Root class="hidden min-w-0 lg:block">
					<Breadcrumb.List class="flex-nowrap">
						<Breadcrumb.Separator />
						{#if router.current.name === 'item'}
							<Breadcrumb.Item><Breadcrumb.Link href={href('queue')}>{t('navQueue')}</Breadcrumb.Link></Breadcrumb.Item>
							<Breadcrumb.Separator /><Breadcrumb.Item><Breadcrumb.Page class="num">{itemProject} · {itemId}</Breadcrumb.Page></Breadcrumb.Item>
						{:else}<Breadcrumb.Item><Breadcrumb.Page>{here ? t(here.label) : ''}</Breadcrumb.Page></Breadcrumb.Item>{/if}
					</Breadcrumb.List>
				</Breadcrumb.Root>
				<div class="ml-auto flex items-center gap-1.5">
					<VersionNotice />
					<AutopilotSwitch />
					{#if session.standby}
						<button onclick={() => standby(false)} class="inline-flex h-8 items-center gap-1.5 rounded-md border border-dashed border-st-needs/60 bg-st-needs/10 px-2 text-xs font-medium text-st-needs hover:bg-st-needs/20" title={session.standbyWhy}>
							<MoonStarIcon class="size-3.5 shrink-0" /><span class="hidden whitespace-nowrap sm:inline">{t('standbyBadge')}</span><span class="hidden whitespace-nowrap xl:inline">· {session.machine}</span>
						</button>
					{/if}
					<button class="grid size-8 place-items-center rounded-md hover:bg-muted md:hidden" onclick={() => (keys.palette = true)} aria-label={t('search')}><SearchIcon class="size-4" /></button>
				</div>
			</header>
			<div id="scroller" class="min-h-0 flex-1 overflow-y-auto overscroll-contain [scrollbar-gutter:stable]">
				{#each paused as p (p.project)}
					<div class="flex flex-wrap items-center gap-x-3 gap-y-2 border-b border-st-needs/30 bg-st-needs/10 px-4 py-2 text-sm">
						<PauseIcon class="size-4 shrink-0 text-st-needs" />
						<span class="min-w-0 flex-1"><strong>{t('pausedBanner', p.project)}</strong> {p.pausedWhy}</span>
						<button class="rounded-md bg-foreground px-2.5 py-1 text-xs font-medium text-background" onclick={() => resumeProject(p.project)}>{t('resumeAll')}</button>
					</div>
				{/each}
				<main id="view" class="w-full p-3 sm:p-4 lg:p-6 min-[1800px]:px-8" tabindex="-1">
					{#if !session.projects.length}
						<p class="text-muted-foreground">{t('noProject')}</p>
					{:else}
						{#key session.scope + router.current.name + router.current.args.join('/')}
							<div class="animate-fade-in">
								{#if router.current.name === 'home'}<Home />
								{:else if router.current.name === 'overview'}<Overview />
								{:else if router.current.name === 'queue'}<Queue />
								{:else if router.current.name === 'item'}<Item project={itemProject} id={itemId} />
								{:else if router.current.name === 'agents'}<Agents />
								{:else if router.current.name === 'accounts'}<Accounts />
								{:else if router.current.name === 'proposals'}<Proposals />
								{:else if router.current.name === 'machines'}<Machines />
								{:else if router.current.name === 'capabilities'}<Capabilities />
								{:else if router.current.name === 'parts'}<Parts />
								{:else if router.current.name === 'metrics'}<Metrics />
								{/if}
							</div>
						{/key}
					{/if}
				</main>
			</div>
			<StatusBar />
		</Sidebar.Inset>
	</Sidebar.Provider>
	<CommandPalette />
	<ShortcutsDialog />
	<DiscussionSheet />
	<LoginSheet />
{/if}

<FormDialog />
