<script lang="ts">
	import * as DropdownMenu from '$lib/components/ui/dropdown-menu';
	import PlayIcon from '@lucide/svelte/icons/play';
	import PauseIcon from '@lucide/svelte/icons/pause';
	import FoldersIcon from '@lucide/svelte/icons/folders';
	import CrosshairIcon from '@lucide/svelte/icons/crosshair';
	import { t } from '$lib/i18n';
	import { cluster } from '$lib/cluster.svelte';
	import { session } from '$lib/session.svelte';
	import { api } from '$lib/api';
	import { act } from './act';

	// Which projects run, at a glance and a click: each one paused or resumed
	// on every machine, one of them alone, or all of them. The machines'
	// switch beside it says where work runs; this one says on what.
	const projects = $derived(
		(cluster.all?.projects || [])
			.map((p) => ({ name: p.project, paused: p.paused, why: p.pausedWhy, working: ['working', 'refining', 'running'].reduce((n, k) => n + Number(p.activity?.[k] || 0), 0) }))
			.sort((a, b) => a.name.localeCompare(b.name))
	);
	const running = $derived(projects.filter((p) => !p.paused).length);
	const nonePaused = $derived(running === projects.length);
	const allPaused = $derived(projects.length > 0 && running === 0);

	const pause = (name: string, why: string) => api.pause({ project: name, why });
	const resume = (name: string) => api.resume({ project: name });

	function toggle(p: { name: string; paused: boolean }) {
		act(() => (p.paused ? resume(p.name) : pause(p.name, t('projectsPausedHere'))), p.paused ? t('projectsResumedOne', p.name) : t('projectsPausedOne', p.name));
	}
	// Only this one runs: it resumes and every other pauses.
	function only(name: string) {
		act(async () => {
			for (const p of projects) {
				if (p.name === name && p.paused) await resume(p.name);
				else if (p.name !== name && !p.paused) await pause(p.name, t('projectsOnlyWhy', name));
			}
		}, t('projectsOnlyDone', name));
	}
	function all(run: boolean) {
		act(async () => {
			for (const p of projects) {
				if (run && p.paused) await resume(p.name);
				else if (!run && !p.paused) await pause(p.name, t('projectsPausedHere'));
			}
		}, run ? t('projectsAllResumed') : t('projectsAllPaused'));
	}
</script>

{#if projects.length > 1}
	<DropdownMenu.Root>
		<DropdownMenu.Trigger>
			{#snippet child({ props })}
				<button {...props} title={t('projectsTitle')}
					class="inline-flex h-8 items-center gap-1.5 rounded-md border px-2 text-xs font-medium transition-colors hover:bg-muted
						{allPaused ? 'border-dashed border-st-needs/60 bg-st-needs/10 text-st-needs' : nonePaused ? 'text-foreground' : 'border-st-needs/40 text-foreground'}">
					{#if allPaused}<PauseIcon class="size-3.5" />{:else}<PlayIcon class="size-3.5 {nonePaused ? 'text-st-done' : 'text-st-needs'}" />{/if}
					<span class="num whitespace-nowrap">{running}/{projects.length}</span>
					<span class="hidden whitespace-nowrap lg:inline">{t('projectsRunning')}</span>
				</button>
			{/snippet}
		</DropdownMenu.Trigger>
		<DropdownMenu.Content align="end" class="w-80">
			<DropdownMenu.Label class="text-xs font-normal text-muted-foreground">{t('projectsLabel', running, projects.length)}</DropdownMenu.Label>
			{#each projects as p (p.name)}
				<DropdownMenu.Item onSelect={() => toggle(p)} class="gap-2.5" title={p.paused && p.why ? p.why : undefined}>
					<span class="relative flex size-2 shrink-0">
						{#if !p.paused && p.working}<span class="absolute inline-flex size-full animate-ping rounded-full bg-st-done opacity-40"></span>{/if}
						<span class="relative inline-flex size-2 rounded-full {p.paused ? 'bg-st-needs' : 'bg-st-done'}"></span>
					</span>
					<span class="min-w-0 flex-1">
						<span class="block truncate">{p.name}{#if p.name === session.project}<span class="text-muted-foreground">{' · ' + t('projectsHere')}</span>{/if}</span>
						<span class="block truncate text-[11px] text-muted-foreground">{p.paused ? p.why || t('projectsPaused') : p.working ? t('projectsWorking', p.working) : t('projectsIdle')}</span>
					</span>
					<span class="shrink-0 text-[11px] font-medium {p.paused ? 'text-st-done' : 'text-st-needs'}">{p.paused ? t('projectsResume') : t('projectsPause')}</span>
				</DropdownMenu.Item>
			{/each}
			<DropdownMenu.Separator />
			<DropdownMenu.Sub>
				<DropdownMenu.SubTrigger><CrosshairIcon />{t('projectsOnly')}</DropdownMenu.SubTrigger>
				<DropdownMenu.SubContent class="w-56">
					{#each projects as p (p.name)}<DropdownMenu.Item onSelect={() => only(p.name)}>{p.name}</DropdownMenu.Item>{/each}
				</DropdownMenu.SubContent>
			</DropdownMenu.Sub>
			<DropdownMenu.Item disabled={nonePaused} onSelect={() => all(true)}><PlayIcon />{t('projectsResumeAll')}</DropdownMenu.Item>
			<DropdownMenu.Item disabled={allPaused} onSelect={() => all(false)}><PauseIcon />{t('projectsPauseAll')}</DropdownMenu.Item>
			<DropdownMenu.Separator />
			<p class="flex gap-2 px-2 py-1.5 text-[11px] leading-snug text-muted-foreground"><FoldersIcon class="mt-0.5 size-3.5 shrink-0" />{t('projectsHelp')}</p>
		</DropdownMenu.Content>
	</DropdownMenu.Root>
{/if}
