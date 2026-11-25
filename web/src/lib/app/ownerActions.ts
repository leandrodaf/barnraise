// The owner's actions the CLI had and the panel lacked: edit, close and
// unlock an item, a tweak, a message to whoever works on an item, the goal,
// the focus, a proposal and an account.
import type { JsonObject } from '@bufbuild/protobuf';
import type { Criterion, Focus, Goal, Item } from '$lib/gen/barnraise/v1/barnraise_pb';
import { api } from '$lib/api';
import { t } from '$lib/i18n';
import { session } from '$lib/session.svelte';
import { act } from './act';
import { dialog } from './dialog.svelte';

const lines = (s: string) => s.split('\n').map((l) => l.trim()).filter(Boolean);
const PRIORITIES: [string, string][] = [['P0', 'P0'], ['P1', 'P1'], ['P2', 'P2'], ['P3', 'P3']];

// The criteria as one line each, "text | check", keeping the ids of the lines that stay.
const criteriaText = (cs: Criterion[]) => cs.map((c) => (c.check ? c.text + ' | ' + c.check : c.text)).join('\n');
function parseCriteria(s: string, old: Criterion[]) {
	return lines(s).map((l) => {
		const i = l.lastIndexOf('|');
		const text = i > 0 ? l.slice(0, i).trim() : l;
		const check = i > 0 ? l.slice(i + 1).trim() : '';
		const was = old.find((c) => c.text === text);
		return { id: was?.id || '', text, check, met: was?.met || false, ...(was?.evidence ? { evidence: was.evidence } : {}) };
	});
}

export function editItem(project: string, it: Item, done?: () => void) {
	dialog.open({
		title: t('editTitle', it.id),
		description: t('editHelp'),
		fields: [
			{ name: 'title', label: t('title'), required: true, value: it.title },
			{ name: 'priority', label: t('priority'), type: 'select', value: 'P' + it.priority, options: PRIORITIES },
			{ name: 'kind', label: t('kind'), value: it.kind, list: ['feature', 'bug', 'refactor', 'chore', 'docs', 'improvement'] },
			{ name: 'area', label: t('area'), value: it.area },
			{ name: 'size', label: t('size'), value: it.size, list: ['XS', 'S', 'M', 'L', 'XL'] },
			{ name: 'risky', label: t('risky'), type: 'select', value: it.risky ? 'yes' : 'no', options: [['no', t('no')], ['yes', t('yes')]] },
			{ name: 'body', label: t('body'), type: 'textarea', rows: 6, value: it.body },
			{ name: 'criteria', label: t('criteria'), type: 'textarea', rows: 5, value: criteriaText(it.criteria), help: t('criteriaHelp'), placeholder: 'WHEN …, THE SYSTEM SHALL … | check' },
			{ name: 'deps', label: t('deps'), value: it.deps.join(', '), placeholder: 'I-12, I-14' },
			{ name: 'reason', label: t('editReason'), help: t('editReasonHelp') }
		],
		submit: t('save'),
		onSubmit: async (v) => {
			const patch: JsonObject = {};
			if (v.title !== it.title) patch.title = v.title;
			if (v.priority !== 'P' + it.priority) patch.priority = v.priority;
			if (v.kind !== it.kind) patch.kind = v.kind;
			if (v.area !== it.area) patch.area = v.area;
			if (v.size !== it.size) patch.size = v.size;
			if ((v.risky === 'yes') !== it.risky) patch.risky = v.risky === 'yes';
			if (v.body !== it.body) patch.body = v.body;
			if (v.criteria.trim() !== criteriaText(it.criteria).trim()) patch.criteria = parseCriteria(v.criteria, it.criteria);
			const deps = v.deps.split(/[\s,]+/).filter(Boolean);
			if (deps.join(',') !== it.deps.join(',')) patch.deps = deps;
			if (!Object.keys(patch).length) return t('editNothing');
			if (v.reason) patch.reason = v.reason;
			await api.updateItem({ project, id: it.id, patch });
			done?.();
			return t('edited', it.id);
		}
	});
}

const CLOSE = ['obsolete', 'duplicate', 'done_by', 'split', 'abandoned'];
export function closeItem(project: string, it: Item) {
	dialog.open({
		title: t('closeTitle', it.id),
		description: t('closeHelp'),
		fields: [
			{ name: 'reason', label: t('closeReason'), type: 'select', value: 'obsolete', options: CLOSE.map((r) => [r, t('close_' + r)] as [string, string]) },
			{ name: 'by', label: t('closeBy'), help: t('closeByHelp'), placeholder: 'I-12' },
			{ name: 'note', label: t('note'), type: 'textarea' }
		],
		submit: t('closeConfirm'),
		danger: true,
		onSubmit: async (v) => {
			await api.closeItem({ project, id: it.id, reason: v.reason, by: v.by.trim(), note: v.note });
			return t('closed', it.id);
		}
	});
}

export function unlockFiles(project: string, it: Item) {
	dialog.open({
		title: t('unlockTitle', it.id),
		description: t('unlockHelp'),
		fields: [
			{ name: 'files', label: t('unlockFiles'), type: 'textarea', required: true, rows: 4, placeholder: 'tests/lodge.gd', value: it.unlocked.join('\n') },
			{ name: 'why', label: t('unlockWhy'), type: 'textarea', required: true }
		],
		submit: t('unlockConfirm'),
		onSubmit: async (v) => {
			await api.unlockFiles({ project, item: it.id, files: lines(v.files), why: v.why });
			return t('unlocked', it.id);
		}
	});
}

export function addTweak(project = session.project, area = '') {
	dialog.open({
		title: t('tweakTitle'),
		description: t('tweakHelp', project),
		fields: [
			{ name: 'area', label: t('area'), value: area },
			{ name: 'text', label: t('tweakText'), type: 'textarea', required: true, rows: 3 }
		],
		submit: t('tweakAdd'),
		onSubmit: async (v) => {
			const r = await api.addTweak({ project, area: v.area, text: v.text });
			return r.item?.id ? t('tweakAttached', r.item.id) : t('tweakAdded');
		}
	});
}

// messageAgent writes to whoever holds the item now: the agent reads it on its next turn.
export function messageAgent(project: string, id: string) {
	dialog.open({
		title: t('msgTitle', id),
		description: t('msgHelp'),
		fields: [{ name: 'text', label: t('msgText'), type: 'textarea', required: true, rows: 4 }],
		submit: t('send'),
		onSubmit: async (v) => {
			await api.sendMessage({ project, message: { to: 'item:' + id, itemId: id, text: v.text } });
			return t('msgSent');
		}
	});
}

// setGoal is the goal's form; a draft from a conversation fills it in.
export function setGoal(project = session.project, draft?: Goal) {
	dialog.open({
		title: t('goalSetTitle', project),
		description: t('goalSetHelp'),
		fields: [
			{ name: 'name', label: t('goalName'), required: true, value: draft?.name },
			{ name: 'doneWhen', label: t('goalDoneWhen'), type: 'textarea', required: true, value: draft?.doneWhen },
			{ name: 'inventory', label: t('goalInventory'), help: t('goalInventoryHelp'), placeholder: 'screens docs/prototype', value: draft?.inventory },
			{ name: 'budget', label: t('goalBudget'), help: t('goalBudgetHelp'), placeholder: '60', value: draft?.budgetPct ? String(draft.budgetPct) : '' }
		],
		submit: t('goalSet'),
		onSubmit: async (v) => {
			const r = await api.setGoal({ project, goal: { name: v.name, doneWhen: v.doneWhen, inventory: v.inventory, budgetPct: Number(v.budget) || 0, active: true } });
			return t('goalIsSet', r.goal?.name || v.name, r.inventory.length);
		}
	});
}

// setFocus is the focus's form; a draft from a conversation fills it in.
export function setFocus(project = session.project, draft?: Focus, until = '') {
	dialog.open({
		title: t('focusSetTitle', project),
		description: t('focusSetHelp'),
		fields: [
			{ name: 'name', label: t('focusName'), required: true, value: draft?.name },
			{ name: 'area', label: t('area'), value: draft?.area },
			{ name: 'items', label: t('focusItemsLabel'), placeholder: 'I-12, I-14', value: draft?.items.join(', ') },
			{ name: 'share', label: t('focusShare'), help: t('focusShareHelp'), placeholder: '70', value: draft?.share ? String(draft.share) : '' },
			{ name: 'until', label: t('focusUntil'), placeholder: '3d', value: until }
		],
		submit: t('focusSet'),
		onSubmit: async (v) => {
			const focus = { name: v.name, area: v.area, items: v.items.split(/[\s,]+/).filter(Boolean), share: Number(v.share) || 0 };
			const r = await api.setFocus({ project, focus, until: v.until });
			const e = r.effect;
			return e ? t('focusIsSet', v.name, e.focusItems, e.parkedElsewhere.length) : t('focusIsSetPlain', v.name);
		}
	});
}

export const clearFocus = (project = session.project) => act(() => api.clearFocus({ project }), t('focusCleared'));

export function propose(project = session.project) {
	dialog.open({
		title: t('proposeTitle', project),
		description: t('proposeHelp'),
		fields: [
			{ name: 'title', label: t('title'), required: true },
			{ name: 'problem', label: t('propProblem'), type: 'textarea', required: true },
			{ name: 'evidence', label: t('propEvidence'), type: 'textarea' },
			{ name: 'impact', label: t('propImpact'), list: ['minor', 'moderate', 'major'] },
			{ name: 'area', label: t('area') },
			{ name: 'kind', label: t('kind'), list: ['feature', 'bug', 'refactor', 'improvement', 'chore'], value: 'improvement' },
			{ name: 'priority', label: t('priority'), type: 'select', value: 'P2', options: PRIORITIES }
		],
		submit: t('proposeSend'),
		onSubmit: async (v) => {
			const r = await api.propose({ project, proposal: { title: v.title, problem: v.problem, evidence: v.evidence, impact: v.impact, area: v.area, kind: v.kind, priority: Number(v.priority.slice(1)) } });
			return r.duplicate ? t('proposeDuplicate', r.duplicate) : t('proposed', r.proposal?.id || '');
		}
	});
}

export function upsertAccount(project = session.project) {
	dialog.open({
		title: t('accAddTitle'),
		description: t('accAddHelp'),
		fields: [
			{ name: 'id', label: t('accId'), required: true, placeholder: 'claude-2' },
			{ name: 'provider', label: t('accProvider'), type: 'select', value: 'claude', options: [['claude', 'claude'], ['codex', 'codex'], ['gemini', 'gemini']] },
			{ name: 'plan', label: t('accPlan'), placeholder: 'max, pro…' },
			{ name: 'machine', label: t('machine'), value: session.machine },
			{ name: 'ceiling', label: t('ceilingLabel'), help: t('accCeilingHelp'), placeholder: '90' },
			{ name: 'configDir', label: t('accConfigDir'), help: t('accConfigDirHelp') }
		],
		submit: t('save'),
		onSubmit: async (v) => {
			await api.upsertAccount({ project, account: { id: v.id.trim(), provider: v.provider, plan: v.plan, machine: v.machine, ceilingPct: Number(v.ceiling) || 0, configDir: v.configDir } });
			return t('accSaved', v.id);
		}
	});
}
