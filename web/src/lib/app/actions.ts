// The owner's actions, shared by the screens and the command palette.
import { api } from '$lib/api';
import { t } from '$lib/i18n';
import { go } from '$lib/router.svelte';
import { session } from '$lib/session.svelte';
import { cluster } from '$lib/cluster.svelte';
import { act } from './act';
import { dialog } from './dialog.svelte';

export function newItem() {
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
			await session.readProjects();
			cluster.read();
			const bad = r.peers.filter((p) => !p.ok);
			if (bad.length) throw new Error(bad.map((p) => p.name + ': ' + p.error).join('; '));
			return on ? t('standbyIsOn', r.stopped) : t('standbyIsOff');
		}
	});
}
