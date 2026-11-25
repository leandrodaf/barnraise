<script lang="ts">
	import * as Command from '$lib/components/ui/command';
	import PlusIcon from '@lucide/svelte/icons/plus';
	import PauseIcon from '@lucide/svelte/icons/pause';
	import PlayIcon from '@lucide/svelte/icons/play';
	import MoonStarIcon from '@lucide/svelte/icons/moon-star';
	import SunMoonIcon from '@lucide/svelte/icons/sun-moon';
	import FolderIcon from '@lucide/svelte/icons/folder';
	import KeyboardIcon from '@lucide/svelte/icons/keyboard';
	import LanguagesIcon from '@lucide/svelte/icons/languages';
	import { locale, setLang } from '$lib/locale.svelte';
	import { t } from '$lib/i18n';
	import { go } from '$lib/router.svelte';
	import { session } from '$lib/session.svelte';
	import { cluster } from '$lib/cluster.svelte';
	import { theme } from '$lib/theme.svelte';
	import { toneOfItem } from '$lib/status';
	import { keys, GO_KEYS } from './keys.svelte';
	import { NAV } from './nav';
	import { clusterItems } from './items';
	import { openItem } from './open';
	import { newItem, pauseProject, resumeProject, standby, talkGoal, talkFocus } from './actions';
	import { setGoal, setFocus, propose, addTweak, upsertAccount } from './ownerActions';
	import TargetIcon from '@lucide/svelte/icons/target';
	import CrosshairIcon from '@lucide/svelte/icons/crosshair';
	import LightbulbIcon from '@lucide/svelte/icons/lightbulb';
	import WandIcon from '@lucide/svelte/icons/wand-sparkles';
	import UserPlusIcon from '@lucide/svelte/icons/user-plus';
	import StatusIcon from './StatusIcon.svelte';

	const rows = $derived(clusterItems(cluster.data));
	const paused = $derived(cluster.data?.projects.find((p) => p.project === session.project)?.paused);
	const run = (fn: () => void) => () => {
		keys.palette = false;
		fn();
	};
</script>

<Command.Dialog bind:open={keys.palette} title={t('palette')} description={t('paletteHelp')}>
	<Command.Input placeholder={t('paletteSearch')} />
	<Command.List class="max-h-[60vh]">
		<Command.Empty>{t('none')}</Command.Empty>
		<Command.Group heading={t('paletteActions')}>
			<Command.Item onSelect={run(newItem)}><PlusIcon />{t('newItem')}<Command.Shortcut>{session.project}</Command.Shortcut></Command.Item>
			{#if paused}<Command.Item onSelect={run(() => resumeProject())}><PlayIcon />{t('resumeAll')}<Command.Shortcut>{session.project}</Command.Shortcut></Command.Item>
			{:else}<Command.Item onSelect={run(() => pauseProject())}><PauseIcon />{t('pauseAll')}<Command.Shortcut>{session.project}</Command.Shortcut></Command.Item>{/if}
			<Command.Item onSelect={run(() => talkGoal())}><TargetIcon />{t('goalSet')}<Command.Shortcut>{session.project}</Command.Shortcut></Command.Item>
			<Command.Item onSelect={run(() => setGoal())}><TargetIcon />{t('goalSetByHand')}<Command.Shortcut>{session.project}</Command.Shortcut></Command.Item>
			<Command.Item onSelect={run(() => talkFocus())}><CrosshairIcon />{t('focusSet')}<Command.Shortcut>{session.project}</Command.Shortcut></Command.Item>
			<Command.Item onSelect={run(() => setFocus())}><CrosshairIcon />{t('focusSetByHand')}<Command.Shortcut>{session.project}</Command.Shortcut></Command.Item>
			<Command.Item onSelect={run(() => propose())}><LightbulbIcon />{t('proposeShort')}<Command.Shortcut>{session.project}</Command.Shortcut></Command.Item>
			<Command.Item onSelect={run(() => addTweak())}><WandIcon />{t('tweakTitle')}<Command.Shortcut>{session.project}</Command.Shortcut></Command.Item>
			<Command.Item onSelect={run(() => upsertAccount())}><UserPlusIcon />{t('accAdd')}</Command.Item>
			<Command.Item onSelect={run(() => standby(!session.standby))}><MoonStarIcon />{session.standby ? t('standbyOff') : t('standbyOn')}</Command.Item>
			<Command.Item onSelect={run(() => theme.cycle())}><SunMoonIcon />{t('theme_' + theme.choice)}</Command.Item>
			<Command.Item value="language idioma english português" onSelect={run(() => setLang(locale.lang === 'en' ? 'pt-BR' : 'en'))}><LanguagesIcon />{locale.lang === 'en' ? 'Português (BR)' : 'English'}</Command.Item>
			<Command.Item onSelect={run(() => (keys.help = true))}><KeyboardIcon />{t('shortcuts')}<Command.Shortcut>?</Command.Shortcut></Command.Item>
		</Command.Group>
		<Command.Group heading={t('screensNav')}>
			{#each NAV as n (n.name)}
				<Command.Item value={'screen ' + t(n.label)} onSelect={run(() => go(n.name))}><n.icon />{t(n.label)}<Command.Shortcut>g {GO_KEYS.find(([, r]) => r === n.name)?.[0]}</Command.Shortcut></Command.Item>
			{/each}
		</Command.Group>
		{#if session.projects.length > 1}
			<Command.Group heading={t('scope')}>
				<Command.Item value={'scope ' + t('allProjects')} onSelect={run(() => session.setScope(''))}><FolderIcon />{t('allProjects')}</Command.Item>
				{#each session.projects as p (p)}<Command.Item value={'project ' + p} onSelect={run(() => session.setScope(p))}><FolderIcon />{p}</Command.Item>{/each}
			</Command.Group>
		{/if}
		<Command.Group heading={t('ovItems')}>
			{#each rows as r (r.project + r.id)}
								<Command.Item value={r.id + ' ' + r.title + ' ' + r.project} onSelect={run(() => openItem(r.project, r.id))}>
					<StatusIcon tone={toneOfItem(r)} /><span class="num text-xs text-muted-foreground">{r.id}</span><span class="truncate">{r.title}</span><Command.Shortcut>{r.project}</Command.Shortcut>
				</Command.Item>
			{/each}
		</Command.Group>
	</Command.List>
</Command.Dialog>
