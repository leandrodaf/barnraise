// How the panel draws what the daemon decided. The daemon says the column
// and the signal of an item (internal/app/board.go); here they only get a
// color family and a shape. No rule about items lives in the panel.
import { t, tm } from './i18n';

export type Tone = 'needs' | 'work' | 'review' | 'queue' | 'done' | 'fail' | 'idle';

// Board columns, in the order they are shown.
export const COLUMNS = ['backlog', 'working', 'needs', 'review', 'queue', 'blocked', 'done'] as const;
export type Column = (typeof COLUMNS)[number];
export const COLUMN_TONE: Record<Column, Tone> = { backlog: 'idle', working: 'work', needs: 'needs', review: 'review', queue: 'queue', blocked: 'fail', done: 'done' };

// Words the daemon uses for agents, accounts, checks and machines.
const TONE: Record<string, Tone> = {
	waiting_owner: 'needs', working: 'work', running: 'work', starting: 'work', refining: 'work',
	queued: 'queue', integrated: 'done', done: 'done', ok: 'done', passed: 'done', green: 'done',
	blocked: 'fail', stalled: 'fail', failed: 'fail', limited: 'fail', expired: 'fail', red: 'fail', yellow: 'needs'
};
export const toneOf = (key: string): Tone => TONE[key] || 'idle';

// Tailwind classes per tone, written out in full so the build keeps them.
export const TEXT: Record<Tone, string> = {
	needs: 'text-st-needs', work: 'text-st-work', review: 'text-st-review', queue: 'text-st-queue',
	done: 'text-st-done', fail: 'text-st-fail', idle: 'text-st-idle'
};
export const BG: Record<Tone, string> = {
	needs: 'bg-st-needs', work: 'bg-st-work', review: 'bg-st-review', queue: 'bg-st-queue',
	done: 'bg-st-done', fail: 'bg-st-fail', idle: 'bg-st-idle'
};
export const SOFT: Record<Tone, string> = {
	needs: 'bg-st-needs/12 text-st-needs', work: 'bg-st-work/12 text-st-work', review: 'bg-st-review/12 text-st-review',
	queue: 'bg-st-queue/12 text-st-queue', done: 'bg-st-done/12 text-st-done', fail: 'bg-st-fail/12 text-st-fail',
	idle: 'bg-st-idle/12 text-st-idle'
};
export const TINT: Record<Tone, string> = {
	needs: 'from-st-needs/10', work: 'from-st-work/10', review: 'from-st-review/10', queue: 'from-st-queue/10',
	done: 'from-st-done/10', fail: 'from-st-fail/10', idle: 'from-st-idle/8'
};

type Board = { column?: string; signal?: { kind: string; arg: string } };

// toneOfItem: the color of the column the daemon put the item in.
export const toneOfItem = (r: Board): Tone => COLUMN_TONE[(r.column || 'backlog') as Column] || 'idle';

// The signal's own color: a failed check is red even in the working column.
const SIGNAL_TONE: Record<string, Tone> = {
	approve: 'needs', question: 'needs', check_failed: 'fail', changes_requested: 'fail', waits_for: 'fail', stalled: 'fail',
	working: 'work', running: 'work', queued: 'queue', integrated: 'done'
};
export const signalTone = (r: Board): Tone => SIGNAL_TONE[r.signal?.kind || ''] || toneOfItem(r);

// signalText words the daemon's signal in the reader's language.
export const signalText = (r: Board): string => (r.signal?.kind ? t('sig_' + r.signal.kind, r.signal.arg || '') : '');

export const columnLabel = (c: string) => t('col_' + c);
export const label = (key: string) => tm('activities', key) || tm('statuses', key);
