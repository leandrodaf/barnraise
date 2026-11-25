// The cluster as the serving daemon sees it. `data` is the scope picked in
// the header, filtered by the daemon (Overview with a project); `all` is
// every project, for what belongs to the whole cluster: the machines. The
// panel never filters or adds up items itself.
import { untrack } from 'svelte';
import { api } from './api';
import type { OverviewResponse, MachineSeries } from './gen/barnraise/v1/barnraise_pb';
import { session } from './session.svelte';

const SOON_MS = 4_000;

class Cluster {
	// Kept as they came (not deep-proxied): every screen only reads them.
	data = $state.raw<OverviewResponse | null>(null);
	all = $state.raw<OverviewResponse | null>(null);
	history = $state.raw<Record<string, MachineSeries>>({});
	error = $state('');
	#seq = 0;
	#timer = 0;
	#lastHistory = 0;
	#running = false;
	#again = false;
	#lastRead = 0;
	#soon = 0;

	// read reads the cluster again; asked while a read runs, it reads once
	// more after it instead of starting another one alongside.
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

	// soon reads again for an event: at most once every few seconds, since
	// every screen and the sidebar draw again from the whole cluster's answer.
	soon() {
		if (this.#soon) return;
		const wait = Math.max(0, this.#lastRead + SOON_MS - Date.now());
		this.#soon = window.setTimeout(() => {
			this.#soon = 0;
			this.read();
		}, wait);
	}

	async #read() {
		if (!session.signedIn) return;
		this.#lastRead = Date.now();
		const mine = ++this.#seq;
		const scope = session.scope;
		session.readProjects().catch(() => {}); // the version and the standby, every read
		try {
			const all = await api.overview({ brief: true, limit: 500, finished: '7d' });
			const data = scope ? await api.overview({ project: scope, brief: true, limit: 500, finished: '7d' }) : all;
			if (mine !== this.#seq || scope !== session.scope) return;
			this.all = all;
			this.data = data;
			this.error = '';
		} catch (e) {
			if (!session.failed(e)) this.error = String(e);
		}
		// Each project knows the machines that work on it: the series of all
		// projects together cover the cluster.
		if (Date.now() - this.#lastHistory > 30_000) {
			this.#lastHistory = Date.now();
			const got = await Promise.all(session.projects.map((project) => api.machineHistory({ project, window: '2h' }).catch(() => null)));
			const by: Record<string, MachineSeries> = {};
			for (const h of got) for (const s of h?.series || []) if (!by[s.machine] || s.samples.length > by[s.machine].samples.length) by[s.machine] = s;
			this.history = by;
		}
	}

	start() {
		clearInterval(this.#timer);
		this.read();
		this.#timer = window.setInterval(() => this.read(), 15_000);
	}
}

export const cluster = new Cluster();

let lastScope: string | null = null;
$effect.root(() => {
	$effect(() => {
		void session.tick;
		const scope = session.scope;
		if (!session.signedIn) return;
		// After an action or in a new scope, at once; for the events, soon.
		const now = scope !== lastScope || session.changes === null;
		lastScope = scope;
		untrack(() => (now ? cluster.read() : cluster.soon()));
	});
	$effect(() => {
		if (session.signedIn) cluster.start();
	});
});
