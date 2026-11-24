import HouseIcon from '@lucide/svelte/icons/house';
import NetworkIcon from '@lucide/svelte/icons/network';
import KanbanIcon from '@lucide/svelte/icons/square-kanban';
import BotIcon from '@lucide/svelte/icons/bot';
import KeyRoundIcon from '@lucide/svelte/icons/key-round';
import LightbulbIcon from '@lucide/svelte/icons/lightbulb';
import ServerIcon from '@lucide/svelte/icons/server';
import ChartIcon from '@lucide/svelte/icons/chart-no-axes-column';
import type { RouteName } from '$lib/router.svelte';

export const NAV: { name: RouteName; label: string; icon: typeof HouseIcon }[] = [
	{ name: 'home', label: 'navHome', icon: HouseIcon },
	{ name: 'overview', label: 'navOverview', icon: NetworkIcon },
	{ name: 'queue', label: 'navQueue', icon: KanbanIcon },
	{ name: 'agents', label: 'navAgents', icon: BotIcon },
	{ name: 'proposals', label: 'navProposals', icon: LightbulbIcon },
	{ name: 'machines', label: 'navMachines', icon: ServerIcon },
	{ name: 'accounts', label: 'navAccounts', icon: KeyRoundIcon },
	{ name: 'metrics', label: 'navMetrics', icon: ChartIcon }
];
