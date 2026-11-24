<script lang="ts">
	import * as Sidebar from '$lib/components/ui/sidebar';
	import * as DropdownMenu from '$lib/components/ui/dropdown-menu';
	import ChevronsUpDownIcon from '@lucide/svelte/icons/chevrons-up-down';
	import LanguagesIcon from '@lucide/svelte/icons/languages';
	import SunMoonIcon from '@lucide/svelte/icons/sun-moon';
	import SunIcon from '@lucide/svelte/icons/sun';
	import MoonIcon from '@lucide/svelte/icons/moon';
	import MonitorIcon from '@lucide/svelte/icons/monitor';
	import KeyboardIcon from '@lucide/svelte/icons/keyboard';
	import LogOutIcon from '@lucide/svelte/icons/log-out';
	import { t } from '$lib/i18n';
	import { locale, setLang, type Lang } from '$lib/locale.svelte';
	import { theme, type ThemeChoice } from '$lib/theme.svelte';
	import { session } from '$lib/session.svelte';
	import { keys } from './keys.svelte';
	import { when } from '$lib/format';

	// The owner's menu: language, theme, shortcuts and sign out, behind one button.
	const sidebar = Sidebar.useSidebar();
</script>

<Sidebar.Menu>
	<Sidebar.MenuItem>
		<DropdownMenu.Root>
			<DropdownMenu.Trigger>
				{#snippet child({ props })}
					<Sidebar.MenuButton size="lg" class="data-[state=open]:bg-sidebar-accent" tooltipContent={t('menu')} {...props}>
						<span class="grid size-8 shrink-0 place-items-center rounded-md border bg-background/60 text-xs font-semibold uppercase">{session.machine.slice(0, 2)}</span>
						<span class="grid min-w-0 flex-1 leading-tight">
							<span class="truncate text-sm font-medium">{t('owner')}</span>
							<span class="num truncate text-[11px] text-muted-foreground">{session.machine} · {locale.lang === 'en' ? 'EN' : 'PT'}</span>
						</span>
						<ChevronsUpDownIcon class="ml-auto size-4 text-muted-foreground" />
					</Sidebar.MenuButton>
				{/snippet}
			</DropdownMenu.Trigger>
			<DropdownMenu.Content class="w-60" side={sidebar.isMobile ? 'top' : 'right'} align="end" sideOffset={6}>
				<DropdownMenu.Label class="space-y-0.5 font-normal">
					<p class="text-xs font-medium">Barnraise <span class="num">v{session.version}</span></p>
					<p class="num text-[11px] text-muted-foreground">{session.buildTime ? t('builtAt', when(session.buildTime)) : t('builtUnknown')}</p>
					<p class="num text-[11px] text-muted-foreground">{t('sbDaemon', session.version, session.machine)}</p>
					{#if session.outdated}<p class="text-[11px] text-st-needs">{t('updateLine', session.newestVersion, session.newestMachine)}</p>{/if}
				</DropdownMenu.Label>
				<DropdownMenu.Separator />
				<DropdownMenu.Sub>
					<DropdownMenu.SubTrigger><LanguagesIcon />{t('language')}<span class="ml-auto text-xs text-muted-foreground">{locale.lang === 'en' ? 'English' : 'Português'}</span></DropdownMenu.SubTrigger>
					<DropdownMenu.SubContent>
						<DropdownMenu.RadioGroup value={locale.lang} onValueChange={(v) => setLang(v as Lang)}>
							<DropdownMenu.RadioItem value="en">English</DropdownMenu.RadioItem>
							<DropdownMenu.RadioItem value="pt-BR">Português (BR)</DropdownMenu.RadioItem>
						</DropdownMenu.RadioGroup>
					</DropdownMenu.SubContent>
				</DropdownMenu.Sub>
				<DropdownMenu.Sub>
					<DropdownMenu.SubTrigger><SunMoonIcon />{t('themeLabel')}<span class="ml-auto text-xs text-muted-foreground">{t('themeShort_' + theme.choice)}</span></DropdownMenu.SubTrigger>
					<DropdownMenu.SubContent>
						<DropdownMenu.RadioGroup value={theme.choice} onValueChange={(v) => theme.set(v as ThemeChoice)}>
							<DropdownMenu.RadioItem value="system"><MonitorIcon />{t('themeShort_system')}</DropdownMenu.RadioItem>
							<DropdownMenu.RadioItem value="light"><SunIcon />{t('themeShort_light')}</DropdownMenu.RadioItem>
							<DropdownMenu.RadioItem value="dark"><MoonIcon />{t('themeShort_dark')}</DropdownMenu.RadioItem>
						</DropdownMenu.RadioGroup>
					</DropdownMenu.SubContent>
				</DropdownMenu.Sub>
				<DropdownMenu.Item onSelect={() => (keys.help = true)}><KeyboardIcon />{t('shortcuts')}<DropdownMenu.Shortcut>?</DropdownMenu.Shortcut></DropdownMenu.Item>
				<DropdownMenu.Separator />
				<DropdownMenu.Item variant="destructive" onSelect={() => session.signOut()}><LogOutIcon />{t('logout')}</DropdownMenu.Item>
			</DropdownMenu.Content>
		</DropdownMenu.Root>
	</Sidebar.MenuItem>
</Sidebar.Menu>
