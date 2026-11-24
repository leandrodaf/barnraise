// The signed-in session: token, projects, the machine that serves the panel,
// and the live event stream that tells every screen when to read again.
import { api, getToken, isAuthError, setToken, store, takeTokenFromURL } from './api';
import type { SessionInfo } from './gen/barnraise/v1/barnraise_pb';

export type LiveMode = 'wait' | 'on' | 'off';

// Every event type the daemon streams; the stream names them without the
// dev.barnraise. prefix.
const EVENT_TYPES = [
	'item.created', 'item.updated', 'item.claimed', 'item.released', 'item.step.started', 'item.step.finished',
	'item.blocked', 'item.waiting_owner', 'item.queued', 'item.integrated', 'item.closed', 'item.paused', 'item.resumed',
	'item.redo', 'item.revalidated', 'item.impacted', 'item.updated_by_change', 'item.obsoleted', 'item.superseded',
	'item.merged_into', 'delivery.submitted', 'review.submitted', 'check.requested', 'check.completed', 'rework.started',
	'proposal.created', 'proposal.updated', 'tweak.added', 'tweak.attached', 'owner.question.asked', 'owner.question.answered',
	'goal.set', 'goal.finished', 'focus.changed', 'system.paused', 'system.resumed', 'account.updated',
	'account.limit.reached', 'machine.updated', 'machine.left', 'machine.worktrees', 'agent.session', 'agent.usage',
	'message.sent', 'message.delivered', 'journal.entry', 'policy.violation.blocked', 'secret.blocked'
];

class Session {
	signedIn = $state(false);
	signInError = $state('');
	booting = $state(true);
	projects = $state<string[]>([]);
	// scope is what the screens show: one project, or every project when empty.
	scope = $state(store.get('scope'));
	// project is the one an action goes to (a new item, a pause): the scope's,
	// or the last one picked when the scope is every project.
	project = $state('');
	machine = $state('');
	version = $state('');
	// The build this page was loaded with: when the daemon answers with
	// another one, it was updated under the page and the page should reload.
	loadedVersion = '';
	reloadNeeded = $state(false);
	buildTime = $state('');
	// The daemon says whether a machine of the cluster runs a newer build.
	outdated = $state(false);
	newestMachine = $state('');
	newestVersion = $state('');
	newestBuildTime = $state('');
	selfUpdate = $state(true);
	standby = $state(false);
	standbyWhy = $state('');
	live = $state<LiveMode>('wait');
	retryIn = $state(0);
	// tick changes when something happened: screens read again on it.
	tick = $state(0);
	pulse = $state(0);
	agents = $state(new Map<string, SessionInfo>());

	#es: EventSource[] = [];
	#lastSeq: Record<string, number> = {};
	#backoff = 1000;
	#timer = 0;
	#countdown = 0;
	#refresh = 0;

	async start() {
		const fromURL = takeTokenFromURL();
		const tok = fromURL || store.get('token');
		if (!tok) {
			this.booting = false;
			return;
		}
		await this.signIn(tok);
	}

	// signIn checks the token with ListProjects; it returns false when refused.
	async signIn(tok: string): Promise<boolean> {
		setToken(tok);
		this.booting = true;
		try {
			await this.readProjects();
		} catch (e) {
			this.booting = false;
			if (isAuthError(e)) {
				setToken('');
				this.signedIn = false;
				return false;
			}
			throw e;
		}
		this.signedIn = true;
		this.signInError = '';
		this.booting = false;
		this.connect();
		return true;
	}

	async readProjects() {
		const r = await api.listProjects({});
		this.projects = r.projects;
		this.machine = r.machine;
		this.version = r.version;
		if (!this.loadedVersion) this.loadedVersion = r.version;
		this.reloadNeeded = this.loadedVersion !== r.version;
		this.buildTime = r.buildTime;
		this.outdated = r.outdated;
		this.newestMachine = r.newestMachine;
		this.newestVersion = r.newestVersion;
		this.newestBuildTime = r.newestBuildTime;
		this.selfUpdate = r.selfUpdate;
		this.standby = r.standby;
		this.standbyWhy = r.standbyWhy;
		const saved = store.get('project');
		if (this.scope && !this.projects.includes(this.scope)) this.scope = '';
		if (this.scope) this.project = this.scope;
		else if (!this.projects.includes(this.project)) this.project = this.projects.includes(saved) ? saved : this.projects[0] || '';
	}

	signOut(msg = '') {
		this.disconnect();
		setToken('');
		this.signedIn = false;
		this.signInError = msg;
	}

	// failed is where every screen sends an error: a refused token signs out.
	failed(e: unknown): boolean {
		if (isAuthError(e)) {
			this.signOut('tokenBad');
			return true;
		}
		return false;
	}

	get scoped(): string[] {
		return this.scope ? [this.scope] : this.projects;
	}

	setScope(p: string) {
		if (p === this.scope) return;
		this.scope = p;
		store.set('scope', p);
		if (p) {
			this.project = p;
			store.set('project', p);
		}
		this.#lastSeq = {};
		this.agents = new Map();
		this.connect();
		this.bump(0);
	}

	// setProject picks the project for actions without changing the scope.
	setProject(p: string) {
		this.project = p;
		store.set('project', p);
	}

	// bump asks the screens to read again, a moment later so that a burst of
	// events makes one read.
	bump(ms = 300) {
		clearTimeout(this.#refresh);
		this.#refresh = window.setTimeout(() => {
			this.tick++;
		}, ms);
	}

	connect() {
		this.disconnect();
		const tok = getToken();
		const list = this.scoped;
		if (!tok || !list.length) return;
		this.live = 'wait';
		let open = 0;
		for (const project of list) {
			const q = new URLSearchParams({ project, token: tok });
			const last = this.#lastSeq[project] || 0;
			if (last > 0) q.set('after_seq', String(last));
			else q.set('backlog', '1');
			const es = new EventSource('/v1/events?' + q.toString());
			this.#es.push(es);
			let replaying = !last;
			const onEvent = (ev: MessageEvent) => {
				if (ev.lastEventId) {
					const n = Number(ev.lastEventId);
					if (n > (this.#lastSeq[project] || 0)) this.#lastSeq[project] = n;
				}
				let ce: any = null;
				try {
					ce = JSON.parse(ev.data);
				} catch {
					/* not JSON */
				}
				const type = ev.type && ev.type !== 'message' ? ev.type : String(ce?.type || '').replace(/^dev\.barnraise\./, '');
				const s = ce?.data?.session;
				if (type === 'agent.session' && s?.agent) {
					const next = new Map(this.agents);
					next.set(s.agent, {
						agent: s.agent, role: s.role || '', account: s.account || '', provider: s.provider || '',
						model: s.model || '', effort: s.effort || '', itemId: s.item_id || '', step: s.step || '',
						sessionId: s.session_id || '', state: s.state || '', detail: s.detail || '', at: s.at || '',
						machine: s.machine || '', since: s.since || '', tokens: BigInt(s.tokens || 0), costUsd: Number(s.cost_usd || 0)
					} as SessionInfo);
					this.agents = next;
				}
				if (replaying) return; // the backlog only fills state; the screen is already fresh
				this.pulse++;
				this.bump();
			};
			es.onmessage = onEvent;
			for (const typ of EVENT_TYPES) es.addEventListener(typ, onEvent as EventListener);
			es.onopen = () => {
				this.#backoff = 1000;
				if (++open >= list.length) this.live = 'on';
				// The ": connected" comment ends the backlog without an event.
				setTimeout(() => (replaying = false), 800);
			};
			es.onerror = () => {
				if (!this.#es.includes(es)) return;
				this.disconnect();
				const wait = this.#backoff;
				this.#backoff = Math.min(this.#backoff * 2, 30000);
				this.retryIn = Math.round(wait / 1000);
				this.live = 'off';
				this.#countdown = window.setInterval(() => {
					if (this.retryIn > 1) this.retryIn--;
				}, 1000);
				this.#timer = window.setTimeout(() => {
					clearInterval(this.#countdown);
					this.connect();
					this.readProjects().catch(() => {});
					this.bump(0);
				}, wait);
			};
		}
	}

	disconnect() {
		clearTimeout(this.#timer);
		clearInterval(this.#countdown);
		for (const es of this.#es) es.close();
		this.#es = [];
	}
}

export const session = new Session();

document.addEventListener('visibilitychange', () => {
	if (!document.hidden && session.signedIn) session.bump(0);
});
