// loader reads a screen's data, and reads it again when the scope changes or
// an event arrives. What was shown stays on screen while the next read runs.
import { errorText } from './api';
import { session } from './session.svelte';

export function loader<T>(read: (projects: string[]) => Promise<T>) {
	const s = $state({ data: undefined as T | undefined, error: '', loading: true });
	let seq = 0;
	const run = async () => {
		const mine = ++seq;
		const projects = session.scoped;
		try {
			const data = await read(projects);
			if (mine !== seq) return;
			s.data = data;
			s.error = '';
		} catch (e) {
			if (mine !== seq || session.failed(e)) return;
			s.error = errorText(e);
		} finally {
			if (mine === seq) s.loading = false;
		}
	};
	let lastScope: string | null = null;
	$effect(() => {
		void session.tick;
		const scope = session.scope + '|' + session.projects.join(',');
		if (scope !== lastScope) {
			if (lastScope !== null) s.data = undefined;
			lastScope = scope;
			s.loading = true;
		}
		run();
	});
	return {
		get data() {
			return s.data;
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
