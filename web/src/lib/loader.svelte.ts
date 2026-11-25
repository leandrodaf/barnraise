// loader reads a screen's data, and reads it again when the scope changes or
// an event arrives. What was shown stays on screen while the next read runs.
//
// One read at a time: what asks for a read while one runs gets one more read
// after it, not a read of its own. Reads that overlap only to be thrown away
// pile up when events come faster than a read takes, and the screen never
// gets an answer. The answer is kept as it came (not deep-proxied): screens
// only read it. What the read itself uses is not watched: a screen whose
// read depends on its own state calls reload when that state changes.
import { untrack } from 'svelte';
import { errorText } from './api';
import { session, type Change } from './session.svelte';

export type LoaderOptions = {
	// relevant says whether the events of a tick can change what the screen
	// shows; without it, every event reads again.
	relevant?: (changes: Change[]) => boolean;
};

export function loader<T>(read: (projects: string[]) => Promise<T>, opts: LoaderOptions = {}) {
	const s = $state({ error: '', loading: true });
	let data = $state.raw<T | undefined>(undefined);
	let seq = 0; // the scope's reads: a read of an old scope is thrown away
	let running = false;
	let again = false;
	const run = async () => {
		if (running) {
			again = true;
			return;
		}
		running = true;
		try {
			do {
				again = false;
				const mine = seq;
				try {
					const got = await read(session.scoped);
					if (mine !== seq) continue;
					data = got;
					s.error = '';
				} catch (e) {
					if (mine !== seq || session.failed(e)) continue;
					s.error = errorText(e);
				} finally {
					if (mine === seq) s.loading = false;
				}
			} while (again);
		} finally {
			running = false;
		}
	};
	let lastScope: string | null = null;
	let lastTick = -1;
	$effect(() => {
		const tick = session.tick;
		const scope = session.scope + '|' + session.projects.join(',');
		if (scope !== lastScope) {
			if (lastScope !== null) data = undefined;
			lastScope = scope;
			seq++;
			s.loading = true;
			lastTick = tick;
			again = running;
			untrack(run);
			return;
		}
		if (tick === lastTick) return;
		lastTick = tick;
		const changes = session.changes;
		if (opts.relevant && changes && !opts.relevant(changes)) return;
		untrack(run);
	});
	return {
		get data() {
			return data;
		},
		get error() {
			return s.error;
		},
		get loading() {
			return s.loading;
		},
		reload: run
	};
}
