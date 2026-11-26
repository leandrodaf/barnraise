// The pieces every conversation with an AI shares in the panel: the owner's
// conversation, an item's agent and the advisor.
import TerminalIcon from '@lucide/svelte/icons/square-terminal';
import FileIcon from '@lucide/svelte/icons/file-text';
import SearchIcon from '@lucide/svelte/icons/search';
import GlobeIcon from '@lucide/svelte/icons/globe';
import PlugIcon from '@lucide/svelte/icons/plug';
import ListChecksIcon from '@lucide/svelte/icons/list-checks';
import WrenchIcon from '@lucide/svelte/icons/wrench';
import PencilIcon from '@lucide/svelte/icons/file-pen-line';
import BrainIcon from '@lucide/svelte/icons/brain';
import FolderSearchIcon from '@lucide/svelte/icons/folder-search';
import MailIcon from '@lucide/svelte/icons/mail';
import { t } from '$lib/i18n';

// One thing said or done. Tool entries carry what the tool was asked
// (input) and what it gave back (result); a tool with no result yet runs.
export type ChatEntry = {
	key: string;
	role: 'user' | 'assistant' | 'tool' | 'note';
	text?: string;
	tool?: string;
	input?: string;
	result?: string;
	failed?: boolean;
	at?: string;
	// The assistant's header: who answered (default: Barnraise), a line of
	// details (model, tokens) and badges.
	author?: string;
	meta?: string;
	badges?: { label: string; tone: 'review' | 'needs' | 'fail' | 'done' }[];
	// note: a line from the system (an error, a limit, the session ended).
	tone?: 'fail' | 'muted';
};

// The tool's kind picks its icon and how it reads.
export function toolLook(name = ''): { icon: typeof WrenchIcon; label: string } {
	const n = name.toLowerCase();
	if (name === 'Bash' || n === 'shell' || n === 'command') return { icon: TerminalIcon, label: 'Terminal' };
	if (name === 'Read' || name === 'LS' || n === 'read') return { icon: FileIcon, label: t('convToolRead') };
	if (name === 'Grep' || name === 'Glob' || n === 'search' || n === 'grep') return { icon: SearchIcon, label: t('convToolSearch') };
	if (n === 'list' || n === 'ls' || n === 'find') return { icon: FolderSearchIcon, label: t('convToolSearch') };
	if (name.startsWith('Web') || n === 'web') return { icon: GlobeIcon, label: 'Web' };
	if (name === 'TodoWrite') return { icon: ListChecksIcon, label: t('convToolPlan') };
	if (name === 'Edit' || name === 'Write' || name === 'MultiEdit' || n === 'edit') return { icon: PencilIcon, label: t('convToolEdit') };
	if (name === 'Thinking') return { icon: BrainIcon, label: t('convToolThinking') };
	if (name.startsWith('mcp__barnraise__')) return { icon: MailIcon, label: 'Barnraise · ' + name.slice(16) };
	if (name.startsWith('mcp__')) return { icon: PlugIcon, label: name.split('__').slice(1).join(' · ') };
	return { icon: WrenchIcon, label: name };
}

export const shortPath = (s = '') => s.replace(/\/home\/[^/\s]+\//g, '~/').replace(/^(command|file_path|path|pattern|url|query|description): /, '');

// Turns: the owner's message, then everything the assistant did and said
// until the next one; tool runs between two texts fold into one group.
export type Part = { kind: 'text'; e: ChatEntry } | { kind: 'tools'; list: ChatEntry[]; key: string } | { kind: 'note'; e: ChatEntry };
export type Turn = { kind: 'user'; e: ChatEntry } | { kind: 'assistant'; parts: Part[]; key: string; at?: string; author?: string; meta?: string; badges?: ChatEntry['badges'] };

export function toTurns(entries: ChatEntry[]): Turn[] {
	const out: Turn[] = [];
	for (const e of entries) {
		if (e.role === 'user') {
			out.push({ kind: 'user', e });
			continue;
		}
		let turn = out[out.length - 1];
		// A new author (the checks after the advisor) starts its own turn.
		if (turn?.kind !== 'assistant' || (e.role === 'assistant' && e.author !== undefined && e.author !== turn.author)) {
			turn = { kind: 'assistant', parts: [], key: e.key, at: e.at, author: e.author, meta: e.meta, badges: e.badges };
			out.push(turn);
		}
		if (e.role === 'tool') {
			const last = turn.parts[turn.parts.length - 1];
			if (last?.kind === 'tools') last.list.push(e);
			else turn.parts.push({ kind: 'tools', list: [e], key: e.key });
		} else if (e.role === 'note') turn.parts.push({ kind: 'note', e });
		else {
			turn.parts.push({ kind: 'text', e });
			if (e.meta) turn.meta = e.meta;
			if (e.badges) turn.badges = e.badges;
			if (e.at && !turn.at) turn.at = e.at;
		}
	}
	return out;
}

export const turnText = (turn: Turn) =>
	turn.kind === 'assistant' ? turn.parts.map((p) => (p.kind === 'text' ? p.e.text || '' : '')).filter(Boolean).join('\n\n') : turn.e.text || '';

// The last answer asks the owner something.
export const asksOwner = (turns: Turn[]) => {
	const last = turns[turns.length - 1];
	return last?.kind === 'assistant' && /\?\s*(\*\*)?\s*$/.test(turnText(last).trim());
};

// A duration in words, short: 12s, 3m 05s, 1h 02m.
export function clock(sec: number): string {
	sec = Math.max(0, Math.round(sec));
	if (sec < 60) return sec + 's';
	const m = Math.floor(sec / 60);
	if (m < 60) return m + 'm ' + String(sec % 60).padStart(2, '0') + 's';
	return Math.floor(m / 60) + 'h ' + String(m % 60).padStart(2, '0') + 'm';
}
