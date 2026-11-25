// What runs on the machines, in detail: the overview without `brief`, which
// keeps the heaviest processes of every worktree and the per-item breakdowns.
// Read only while a screen that shows it is open, and more often than the
// cluster's brief overview, since processes come and go in seconds.
import { untrack } from 'svelte';
import { api } from './api';
import type { OverviewResponse, ProcInfo, WorktreeInfo } from './gen/barnraise/v1/barnraise_pb';
import { session } from './session.svelte';

// The activities whose items can hold processes right now.
const LIVE = ['working', 'refining', 'running', 'stalled', 'idle', 'waiting_owner', 'blocked', 'queued', 'ready'];

export type ProcRow = ProcInfo & { machine: string; project: string; worktree: string; wkind: string; itemId: string; path: string };

class Procs {
	data = $state.raw<OverviewResponse | null>(null);
	error = $state('');
	at = $state(0);
	#users = 0;
	#timer = 0;
	#seq = 0;
	#running = false;
	#again = false;

	// read reads again; asked while a read runs, it reads once more after it.
	async read() {
		if (this.#running) {
			this.#again = true;
			return;
		}
		this.#running = true;
		try {
			do {
				this.#again = false;
				await this.#read();
			} while (this.#again);
		} finally {
			this.#running = false;
		}
	}

	async #read() {
		if (!session.signedIn) return;
		const mine = ++this.#seq;
		const scope = session.scope;
		try {
			const d = await api.overview({ project: session.scope, activity: LIVE, limit: 300 });
			if (mine !== this.#seq || scope !== session.scope) return;
			this.data = d;
			this.error = '';
			this.at = Date.now();
		} catch (e) {
			if (!session.failed(e)) this.error = String(e);
		}
	}

	// use keeps the detailed read going while the calling component lives.
	use() {
		$effect(() => {
			void session.scope;
			void session.tick;
			untrack(() => this.read());
		});
		$effect(() => {
			if (this.#users++ === 0) this.#timer = window.setInterval(() => this.read(), 5_000);
			return () => {
				if (--this.#users === 0) clearInterval(this.#timer);
			};
		});
	}
}

export const procs = new Procs();

// Every worktree of the cluster with what runs there.
export function worktreesOf(o: OverviewResponse | null): WorktreeInfo[] {
	return (o?.machines || []).flatMap((m) => m.worktrees);
}

// Every process the machines named, each with where it runs.
export function procRows(o: OverviewResponse | null): ProcRow[] {
	const out: ProcRow[] = [];
	for (const w of worktreesOf(o))
		for (const p of w.procs?.top || [])
			out.push({ ...p, machine: w.machine, project: w.project, worktree: w.name, wkind: w.kind, itemId: w.itemId, path: w.path } as ProcRow);
	return out;
}

// The colors of the process kinds the machines report.
export const KIND_TONE: Record<string, string> = {
	agent: 'review', test: 'done', browser: 'work', build: 'needs', emulator: 'queue',
	runtime: 'work', container: 'queue', git: 'idle', shell: 'idle', other: 'idle'
};
export const kindTone = (k: string) => (KIND_TONE[k] || 'idle') as 'review';
