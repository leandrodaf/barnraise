// The dependency graph of the items: what waits for what. An item's deps
// must be integrated before its step goes on; the daemon sends both the deps
// and the ones still missing (blockedBy).
import type { Row } from './items';

export type DepNode = {
	key: string; // project/id
	project: string;
	id: string;
	row?: Row; // absent: a dependency outside the overview's window (landed long ago)
	layer: number;
	order: number;
};
export type DepEdge = { from: string; to: string; waiting: boolean }; // from: the dependency, to: the item that waits

const keyOf = (project: string, id: string) => project + '/' + id;
const done = (r?: Row) => !r || r.status === 'integrated' || r.status === 'closed';

// dependents maps each item to the open items that wait for it.
export function dependents(rows: Row[]): Map<string, Row[]> {
	const m = new Map<string, Row[]>();
	for (const r of rows) {
		if (done(r)) continue;
		for (const d of r.blockedBy || []) {
			const k = keyOf(r.project, d);
			m.set(k, [...(m.get(k) || []), r]);
		}
	}
	return m;
}

// depGraph lays the items out in layers, left to right: an item stands one
// layer after the last of its dependencies. Only items in a dependency
// count, and with focus only the chain of that item (what it waits for and
// what waits for it).
export function depGraph(rows: Row[], focus?: string, withDone = true): { nodes: DepNode[]; edges: DepEdge[]; layers: number } {
	const byKey = new Map(rows.map((r) => [keyOf(r.project, r.id), r]));
	const deps = new Map<string, string[]>(); // key -> keys it depends on
	for (const r of rows) {
		if (!r.deps?.length) continue;
		if (done(r) && !withDone) continue;
		deps.set(keyOf(r.project, r.id), r.deps.map((d) => keyOf(r.project, d)));
	}
	let keys = new Set<string>();
	for (const [k, ds] of deps) {
		const r = byKey.get(k);
		// A finished item and its finished deps are history, unless focused.
		const open = !done(r) || ds.some((d) => !done(byKey.get(d)));
		if (!open && !focus) continue;
		keys.add(k);
		for (const d of ds) keys.add(d);
	}
	if (focus) {
		const up = new Map<string, string[]>(); // key -> keys that wait for it
		for (const [k, ds] of deps) for (const d of ds) up.set(d, [...(up.get(d) || []), k]);
		const chain = new Set<string>([focus]);
		const walk = (k: string, next: (k: string) => string[]) => {
			for (const n of next(k)) if (!chain.has(n)) (chain.add(n), walk(n, next));
		};
		walk(focus, (k) => deps.get(k) || []);
		walk(focus, (k) => up.get(k) || []);
		keys = chain.size > 1 ? chain : new Set();
	}
	if (!withDone) for (const k of [...keys]) if (done(byKey.get(k)) && ![...keys].some((o) => deps.get(o)?.includes(k))) keys.delete(k);

	// Layers: the longest chain of dependencies before each item.
	const layer = new Map<string, number>();
	const visiting = new Set<string>();
	const depth = (k: string): number => {
		if (layer.has(k)) return layer.get(k)!;
		if (visiting.has(k)) return 0; // a cycle: drawn, not followed
		visiting.add(k);
		let l = 0;
		for (const d of deps.get(k) || []) if (keys.has(d)) l = Math.max(l, depth(d) + 1);
		visiting.delete(k);
		layer.set(k, l);
		return l;
	};
	for (const k of keys) depth(k);
	const layers = keys.size ? Math.max(...[...keys].map((k) => layer.get(k)!)) + 1 : 0;

	// Order inside a layer: priority first, then twice by the mean place of
	// the neighbours, so lines cross less.
	const cols: string[][] = Array.from({ length: layers }, () => []);
	for (const k of keys) cols[layer.get(k)!].push(k);
	const prio = (k: string) => byKey.get(k)?.priority ?? 9;
	for (const c of cols) c.sort((a, b) => prio(a) - prio(b) || a.localeCompare(b, undefined, { numeric: true }));
	const pos = new Map<string, number>();
	const place = () => cols.forEach((c) => c.forEach((k, i) => pos.set(k, i)));
	place();
	const waitsOn = (k: string) => (deps.get(k) || []).filter((d) => keys.has(d));
	const waitedBy = (k: string) => [...keys].filter((o) => deps.get(o)?.includes(k));
	for (let pass = 0; pass < 2; pass++) {
		for (let l = 1; l < layers; l++) sortBy(cols[l], (k) => mean(waitsOn(k).map((d) => pos.get(d)!)), pos);
		place();
		for (let l = layers - 2; l >= 0; l--) sortBy(cols[l], (k) => mean(waitedBy(k).map((d) => pos.get(d)!)), pos);
		place();
	}

	const nodes: DepNode[] = [...keys].map((k) => {
		const r = byKey.get(k);
		const [project, id] = [k.slice(0, k.lastIndexOf('/')), k.slice(k.lastIndexOf('/') + 1)];
		return { key: k, project, id, row: r, layer: layer.get(k)!, order: pos.get(k)! };
	});
	const edges: DepEdge[] = [];
	for (const k of keys) for (const d of waitsOn(k)) edges.push({ from: d, to: k, waiting: !done(byKey.get(d)) });
	return { nodes, edges, layers };
}

const mean = (xs: number[]) => (xs.length ? xs.reduce((a, b) => a + b, 0) / xs.length : NaN);

function sortBy(c: string[], score: (k: string) => number, pos: Map<string, number>) {
	const s = new Map(c.map((k) => [k, score(k)]));
	c.sort((a, b) => {
		const x = s.get(a)!, y = s.get(b)!;
		return (isNaN(x) ? pos.get(a)! : x) - (isNaN(y) ? pos.get(b)! : y);
	});
}
