// The owner's actions, shared by the screens and the command palette.
import { discussion } from './discussion.svelte';
import { api } from '$lib/api';
import { t } from '$lib/i18n';
import { go } from '$lib/router.svelte';
import { session } from '$lib/session.svelte';
import { cluster } from '$lib/cluster.svelte';
import { act } from './act';
import { dialog } from './dialog.svelte';
import { pendingStandby } from './standby.svelte';
import { rest } from '$lib/rest';

// machineStandby switches one machine; the one serving the panel stays as it is.
const machineStandby = (name: string, on: boolean, why: string) =>
	rest<{ on?: boolean; peers?: { name: string; ok?: boolean; error?: string }[] }>('POST', '/v1/machines/' + encodeURIComponent(name) + '/standby', {}, { on, why });

// newItem opens a conversation with the advisor to ask for work: it reads
// the code, answers, and writes the item when the owner is happy with it.
export function newItem() {
	discussion.show(session.project, chatId('C'), t('chatTitle'));
}

// talkGoal and talkFocus open a conversation to shape the goal (CG-…) or the
// focus (CF-…): the advisor drafts it, and the owner reviews it in the form.
// With every project on screen, it first asks which project: the goal of
// one is never shaped in another's conversation.
export const talkGoal = (project?: string) => talk('CG', 'chatGoalTitle', 'goalPickProject', project);
export const talkFocus = (project?: string) => talk('CF', 'chatFocusTitle', 'focusPickProject', project);

function talk(kind: string, title: string, ask: string, project?: string) {
	const open = (p: string) => discussion.show(p, chatId(kind), t(title));
	if (project || session.scope || session.projects.length <= 1) return open(project || session.project);
	dialog.open({
		title: t(title),
		description: t(ask),
		fields: [{ name: 'project', label: t('project'), type: 'select', value: session.project, options: session.projects.map((p) => [p, p] as [string, string]) }],
		submit: t('pickProject'),
		onSubmit: async (v) => open(v.project)
	});
}

const chatId = (kind: string) => kind + '-' + Array.from(crypto.getRandomValues(new Uint8Array(5)), (b) => 'ABCDEFGHJKLMNPQRSTUVWXYZ23456789'[b % 32]).join('');

// newItemByHand is the form, for who already knows the item to the letter.
export function newItemByHand() {
	// With every project on screen, the form asks which one.
	const pick = !session.scope && session.projects.length > 1;
	dialog.open({
		title: t('newItem'),
		description: pick ? t('newItemPick') : t('newItemHelp', session.project),
		fields: [
			...(pick ? [{ name: 'project', label: t('project'), type: 'select' as const, value: session.project, options: session.projects.map((p) => [p, p] as [string, string]) }] : []),
			{ name: 'title', label: t('title'), required: true },
			{ name: 'kind', label: t('kind'), list: ['feature', 'bug', 'refactor', 'chore', 'docs'], value: 'feature' },
			{ name: 'priority', label: t('priority'), type: 'select', value: 'P2', options: [['P0', 'P0'], ['P1', 'P1'], ['P2', 'P2'], ['P3', 'P3']] },
			{ name: 'area', label: t('area') },
			{ name: 'body', label: t('body'), type: 'textarea', rows: 4 },
			{ name: 'criteria', label: t('criteria'), type: 'textarea', rows: 4, help: t('criteriaHelp'), placeholder: 'WHEN …, THE SYSTEM SHALL … | check' }
		],
		submit: t('create'),
		onSubmit: async (v) => {
			const criteria = v.criteria.split('\n').map((l) => l.trim()).filter(Boolean).map((l) => {
				const i = l.lastIndexOf('|');
				return i > 0 ? { text: l.slice(0, i).trim(), check: l.slice(i + 1).trim() } : { text: l };
			});
			const project = v.project || session.project;
			session.setProject(project);
			const r = await api.createItem({ project, title: v.title, kind: v.kind, priority: v.priority, area: v.area, body: v.body, criteria });
			if (r.item?.id) {
				go('item', project, r.item.id);
				return t('created', r.item.id);
			}
			if (r.proposal?.id) return t('createdProposal', r.proposal.id, r.note || '');
			return r.note || t('done');
		}
	});
}

export function pauseProject(project = session.project) {
	dialog.open({
		title: t('pauseTitleOf', project),
		description: t('pauseHelp'),
		fields: [{ name: 'why', label: t('pauseWhy'), type: 'textarea', required: true }],
		submit: t('pauseConfirm'),
		danger: true,
		onSubmit: async (v) => {
			await api.pause({ project, why: v.why });
			return t('paused');
		}
	});
}

export const resumeProject = (project = session.project) => act(() => api.resume({ project }), t('resumed'));

// connectedPeers are the other machines this daemon reaches now.
const connectedPeers = () => api.listPeers({}).then((r) => r.peers.filter((p) => p.connected).map((p) => p.name)).catch(() => [] as string[]);

// standbyMachine switches one machine of the cluster: this one as standby()
// does, another one through this daemon, leaving this machine as it is.
export function standbyMachine(name: string, on: boolean) {
	if (!on) {
		// Waking stops nothing: no question asked.
		return act(async () => {
			const r = await machineStandby(name, false, '');
			pendingStandby.set([name], false);
			await session.readProjects();
			cluster.read();
			const bad = (r.peers || []).filter((p) => !p.ok);
			if (bad.length) throw new Error(bad.map((p) => p.name + ': ' + p.error).join('; '));
		}, t('mStandbyIsOff', name));
	}
	if (name === session.machine) return standby(on);
	dialog.open({
		title: on ? t('mStandbyOnTitle', name) : t('mStandbyOffTitle', name),
		description: on ? t('standbyOnHelp') : t('standbyOffHelp'),
		fields: on ? [{ name: 'why', label: t('standbyWhy'), type: 'textarea' as const }] : [],
		submit: on ? t('standbyOn') : t('standbyOff'),
		danger: on,
		onSubmit: async (v) => {
			const r = await machineStandby(name, on, v.why || '');
			pendingStandby.set([name], on);
			cluster.read();
			const bad = (r.peers || []).filter((p) => !p.ok);
			if (bad.length) throw new Error(bad.map((p) => p.name + ': ' + p.error).join('; '));
			return on ? t('mStandbyIsOn', name) : t('mStandbyIsOff', name);
		}
	});
}

// clusterStandby switches every machine at once: this one and every connected one.
export async function clusterStandby(on: boolean) {
	const peers = await connectedPeers();
	const names = [session.machine, ...peers].filter(Boolean);
	if (!on) {
		return act(async () => {
			const r = await api.setStandby({ on: false, why: '', peers });
			pendingStandby.set(names, false);
			await session.readProjects();
			cluster.read();
			const bad = r.peers.filter((p) => !p.ok);
			if (bad.length) throw new Error(bad.map((p) => p.name + ': ' + p.error).join('; '));
		}, t('allStandbyIsOff', names.length));
	}
	dialog.open({
		title: on ? t('allStandbyOnTitle', names.length) : t('allStandbyOffTitle', names.length),
		description: (on ? t('allStandbyOnHelp') : t('standbyOffHelp')) + ' ' + t('allStandbyWho', names.join(', ')),
		fields: on ? [{ name: 'why', label: t('standbyWhy'), type: 'textarea' as const }] : [],
		submit: on ? t('allStandbyOn') : t('allStandbyOff'),
		danger: on,
		onSubmit: async (v) => {
			const r = await api.setStandby({ on, why: v.why || '', peers });
			pendingStandby.set(names, on);
			await session.readProjects();
			cluster.read();
			const bad = r.peers.filter((p) => !p.ok);
			if (bad.length) throw new Error(bad.map((p) => p.name + ': ' + p.error).join('; '));
			return on ? t('allStandbyIsOn', names.length, r.stopped) : t('allStandbyIsOff', names.length);
		}
	});
}

// stopMachine stops the daemon of another machine; it starts again only there.
export function stopMachine(name: string) {
	dialog.open({
		title: t('admStopTitle', name),
		description: t('admStopHelp'),
		fields: [],
		submit: t('admStop'),
		danger: true,
		onSubmit: async () => {
			const r = await api.stopPeers({ names: [name] });
			setTimeout(() => cluster.read(), 1500);
			return t('admStopped', r.results.filter((x) => x.stopped).map((x) => x.name).join(', ') || '—');
		}
	});
}

// standby switches this machine, and the connected ones when the owner says so.
export async function standby(on: boolean) {
	const peers = await api.listPeers({}).then((r) => r.peers.filter((p) => p.connected).map((p) => p.name)).catch(() => [] as string[]);
	dialog.open({
		title: on ? t('standbyOnTitle') : t('standbyOffTitle'),
		description: on ? t('standbyOnHelp') : t('standbyOffHelp'),
		fields: [
			...(on ? [{ name: 'why', label: t('standbyWhy'), type: 'textarea' as const }] : []),
			...(peers.length
				? [{ name: 'peers', label: t('standbyPeers'), type: 'select' as const, value: 'here',
					options: [['here', t('standbyHere', session.machine)], ['all', t('standbyAll', peers.join(', '))]] as [string, string][] }]
				: [])
		],
		submit: on ? t('standbyOn') : t('standbyOff'),
		danger: on,
		onSubmit: async (v) => {
			const r = await api.setStandby({ on, why: v.why || '', peers: v.peers === 'all' ? peers : [] });
			pendingStandby.set([session.machine, ...(v.peers === 'all' ? peers : [])], on);
			await session.readProjects();
			cluster.read();
			const bad = r.peers.filter((p) => !p.ok);
			if (bad.length) throw new Error(bad.map((p) => p.name + ': ' + p.error).join('; '));
			return on ? t('standbyIsOn', r.stopped) : t('standbyIsOff');
		}
	});
}
