// Items of the overview, with their project, and the groups the sidebar and
// Home show: by the column the daemon gave each item.
import type { ItemOverview, OverviewResponse } from '$lib/gen/barnraise/v1/barnraise_pb';
import { parseTime } from '$lib/format';

export type Row = ItemOverview & { project: string };

export function clusterItems(o: OverviewResponse | null): Row[] {
	return (o?.projects || []).flatMap((p) => p.items.map((it) => ({ ...it, project: p.project }) as Row));
}

const at = (s?: string) => parseTime(s)?.getTime() || 0;

export function groups(rows: Row[]) {
	return {
		needs: rows.filter((r) => r.needsOwner).sort((a, b) => at(a.updatedAt) - at(b.updatedAt)),
		working: rows.filter((r) => r.column === 'working' && (r.activity === 'working' || r.activity === 'running')).sort((a, b) => at(b.stepStartedAt) - at(a.stepStartedAt)),
		blocked: rows.filter((r) => r.column === 'blocked'),
		recent: rows.filter((r) => r.status === 'integrated').sort((a, b) => at(b.integratedAt) - at(a.integratedAt))
	};
}
