import HouseIcon from '@lucide/svelte/icons/house';
import NetworkIcon from '@lucide/svelte/icons/network';
import KanbanIcon from '@lucide/svelte/icons/square-kanban';
import BotIcon from '@lucide/svelte/icons/bot';
import KeyRoundIcon from '@lucide/svelte/icons/key-round';
import LightbulbIcon from '@lucide/svelte/icons/lightbulb';
import ServerIcon from '@lucide/svelte/icons/server';
import BlocksIcon from '@lucide/svelte/icons/blocks';
import BoxesIcon from '@lucide/svelte/icons/boxes';
import ChartIcon from '@lucide/svelte/icons/chart-no-axes-column';
import ActivityIcon from '@lucide/svelte/icons/activity';
import RadioIcon from '@lucide/svelte/icons/radio';
import SettingsIcon from '@lucide/svelte/icons/settings-2';
import FileTextIcon from '@lucide/svelte/icons/file-text';
import type { RouteName } from '$lib/router.svelte';

export type NavEntry = { name: RouteName; label: string; icon: typeof HouseIcon };

// The screens in groups, the daily ones first: what needs the owner and the
// work in flight; then the project's shape; then the cluster's machinery,
// visited when something is off.
export const NAV_GROUPS: { label: string; items: NavEntry[] }[] = [
	{
		label: 'navGroupDaily',
		items: [
			{ name: 'home', label: 'navHome', icon: HouseIcon },
			{ name: 'queue', label: 'navQueue', icon: KanbanIcon },
			{ name: 'proposals', label: 'navProposals', icon: LightbulbIcon },
			{ name: 'agents', label: 'navAgents', icon: BotIcon },
			{ name: 'activity', label: 'navActivity', icon: RadioIcon }
		]
	},
	{
		label: 'navGroupProject',
		items: [
			{ name: 'parts', label: 'navParts', icon: BoxesIcon },
			{ name: 'metrics', label: 'navMetrics', icon: ChartIcon },
			{ name: 'documents', label: 'navDocuments', icon: FileTextIcon }
		]
	},
	{
		label: 'navGroupCluster',
		items: [
			{ name: 'overview', label: 'navOverview', icon: NetworkIcon },
			{ name: 'machines', label: 'navMachines', icon: ServerIcon },
			{ name: 'processes', label: 'navProcesses', icon: ActivityIcon },
			{ name: 'accounts', label: 'navAccounts', icon: KeyRoundIcon },
			{ name: 'capabilities', label: 'navCapabilities', icon: BlocksIcon },
			{ name: 'admin', label: 'navAdmin', icon: SettingsIcon }
		]
	}
];

// NAV is every screen in the same order, for the palette and the shortcuts.
export const NAV: NavEntry[] = NAV_GROUPS.flatMap((g) => g.items);
