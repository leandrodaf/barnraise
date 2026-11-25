// What the owner just switched, until the machines' next heartbeat says so:
// the screens show the new state at once instead of a few seconds later.
class PendingStandby {
	map = $state<Record<string, { on: boolean; until: number }>>({});
	set(names: string[], on: boolean) {
		const until = Date.now() + 45_000;
		for (const n of names) this.map[n] = { on, until };
	}
	// of is a machine's standby: the pending switch while it lasts, else what it reported.
	of(m: { name: string; standby: boolean }): boolean {
		const p = this.map[m.name];
		if (!p || Date.now() > p.until) return m.standby;
		if (p.on === m.standby) {
			queueMicrotask(() => delete this.map[m.name]);
		}
		return p.on;
	}
}
export const pendingStandby = new PendingStandby();
