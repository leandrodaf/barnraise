// The cluster as the serving daemon sees it. `data` is the scope picked in
// the header, filtered by the daemon (Overview with a project); `all` is
// every project, for what belongs to the whole cluster: the machines. The
// panel never filters or adds up items itself.
import { api } from './api';
import type { OverviewResponse, MachineSeries } from './gen/barnraise/v1/barnraise_pb';
import { session } from './session.svelte';

class Cluster {
	data = $state<OverviewResponse | null>(null);
	all = $state<OverviewResponse | null>(null);
	history = $state<Record<string, MachineSeries>>({});
	error = $state('');
	#seq = 0;
	#timer = 0;
	#lastHistory = 0;

	async read() {
		if (!session.signedIn) return;
		const mine = ++this.#seq;
		const scope = session.scope;
		session.readProjects().catch(() => {}); // the version and the standby, every read
		try {
			const all = await api.overview({ brief: true, limit: 500, finished: '7d' });
			const data = scope ? await api.overview({ project: scope, brief: true, limit: 500, finished: '7d' }) : all;
			if (mine !== this.#seq) return;
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

$effect.root(() => {
	$effect(() => {
		void session.tick;
		void session.scope;
		if (session.signedIn) cluster.read();
	});
	$effect(() => {
		if (session.signedIn) cluster.start();
	});
});
