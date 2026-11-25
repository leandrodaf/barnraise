// Items of the overview, with their project, and the groups the sidebar and
// Home show: by the column the daemon gave each item.
import type { ItemOverview, OverviewResponse } from '$lib/gen/barnraise/v1/barnraise_pb';
import { parseTime } from '$lib/format';

export type Row = ItemOverview & { project: string };

export function clusterItems(o: OverviewResponse | null): Row[] {
	return (o?.projects || []).flatMap((p) => p.items.map((it) => ({ ...it, project: p.project }) as Row));
}

const at = (s?: string) => parseTime(s)?.getTime() || 0;

// What the owner sees at a glance. Every open item is in exactly one group,
// so none disappears from the Home and the sidebar: a draft or a ready item
// waits in upNext, an item between steps or in the merge queue too, and one
// an agent works on is in working whatever its column (a review is work).
const ACTIVE = new Set(['working', 'running', 'refining']);
const active = (r: Row) => ACTIVE.has(r.activity);

export function groups(rows: Row[]) {
	const open = rows.filter((r) => r.column !== 'done' && !r.needsOwner);
	return {
		needs: rows.filter((r) => r.needsOwner).sort((a, b) => at(a.updatedAt) - at(b.updatedAt)),
		working: open.filter(active).sort((a, b) => at(b.stepStartedAt) - at(a.stepStartedAt)),
		blocked: open.filter((r) => !active(r) && r.column === 'blocked'),
		upNext: open.filter((r) => !active(r) && r.column !== 'blocked').sort((a, b) => a.priority - b.priority || at(a.createdAt) - at(b.createdAt)),
		recent: rows.filter((r) => r.status === 'integrated').sort((a, b) => at(b.integratedAt) - at(a.integratedAt))
	};
}
