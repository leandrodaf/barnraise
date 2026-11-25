<script lang="ts">
	import { t } from '$lib/i18n';
	import { TEXT, toneOfItem, signalTone, signalText } from '$lib/status';
	import { session } from '$lib/session.svelte';
	import type { Row } from './items';
	import { depGraph, type DepNode } from './deps';
	import { openItem } from './open';
	import StatusIcon from './StatusIcon.svelte';
	import Empty from './Empty.svelte';

	// rows: the items to draw from; focus: project/id to draw only that
	// item's chain (the item page); compact: smaller cards.
	let { rows, focus, compact = false }: { rows: Row[]; focus?: string; compact?: boolean } = $props();

	const g = $derived(depGraph(rows, focus));
	const W = $derived(compact ? 196 : 232);
	const H = $derived(compact ? 54 : 66);
	const GX = 72;
	const GY = 14;
	const PAD = 8;
	const x = (n: DepNode) => PAD + n.layer * (W + GX);
	const y = (n: DepNode) => PAD + n.order * (H + GY);
	const byKey = $derived(new Map(g.nodes.map((n) => [n.key, n])));
	const width = $derived(PAD * 2 + g.layers * W + Math.max(0, g.layers - 1) * GX);
	const height = $derived(PAD * 2 + Math.max(1, ...g.nodes.map((n) => n.order + 1)) * (H + GY) - GY);

	// Hovering an item lights its whole chain: what it waits for and what waits for it.
	let hover = $state<string | null>(null);
	const lit = $derived.by(() => {
		if (!hover) return null;
		const s = new Set([hover]);
		const walk = (k: string, up: boolean) => {
			for (const e of g.edges) {
				const [a, b] = up ? [e.to, e.from] : [e.from, e.to];
				if (a === k && !s.has(b)) (s.add(b), walk(b, up));
			}
		};
		walk(hover, true);
		walk(hover, false);
		return s;
	});
	const path = (from: DepNode, to: DepNode) => {
		const x1 = x(from) + W, y1 = y(from) + H / 2, x2 = x(to) - 4, y2 = y(to) + H / 2;
		const c = Math.max(24, (x2 - x1) / 2);
		return `M${x1},${y1} C${x1 + c},${y1} ${x2 - c},${y2} ${x2},${y2}`;
	};
	const waitingFor = (n: DepNode) => g.edges.filter((e) => e.to === n.key && e.waiting).length;
	const holding = (n: DepNode) => g.edges.filter((e) => e.from === n.key && e.waiting).length;
</script>

{#if !g.nodes.length}
	<Empty text={focus ? t('depNoneItem') : t('depNone')} />
{:else}
	<div class="space-y-2">
		<p class="flex flex-wrap items-center gap-x-4 gap-y-1 text-[11px] text-muted-foreground">
			<span class="flex items-center gap-1.5"><svg width="28" height="8" aria-hidden="true"><path d="M0,4 H28" class="text-st-needs" stroke="currentColor" stroke-width="2" stroke-dasharray="5 3" /></svg>{t('depLegendWaits')}</span>
			<span class="flex items-center gap-1.5"><svg width="28" height="8" aria-hidden="true"><path d="M0,4 H28" class="text-muted-foreground/50" stroke="currentColor" stroke-width="1.5" /></svg>{t('depLegendLanded')}</span>
			<span>{t('depLegendRead')}</span>
		</p>
		<div class="scroll-x-visible overflow-auto rounded-xl border bg-surface-1/60" style="max-height:{focus ? 360 : 640}px">
			<div class="relative" style="width:{width}px;height:{height}px">
				<svg class="absolute inset-0" {width} {height} aria-hidden="true">
					<defs>
						{#each ['wait', 'done'] as m (m)}
							<marker id={'dep-' + m} viewBox="0 0 8 8" refX="7" refY="4" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
								<path d="M0,0 L8,4 L0,8 z" fill="currentColor" class={m === 'wait' ? 'text-st-needs' : 'text-muted-foreground/50'} />
							</marker>
						{/each}
					</defs>
					{#each g.edges as e (e.from + '>' + e.to)}
						{@const a = byKey.get(e.from)}
						{@const b = byKey.get(e.to)}
						{#if a && b}
							<path
								d={path(a, b)}
								fill="none"
								stroke="currentColor"
								class="transition-opacity {e.waiting ? 'text-st-needs' : 'text-muted-foreground/50'} {lit && !(lit.has(e.from) && lit.has(e.to)) ? 'opacity-15' : ''}"
								stroke-width={e.waiting ? 2 : 1.5}
								stroke-dasharray={e.waiting ? '6 4' : undefined}
								marker-end={'url(#dep-' + (e.waiting ? 'wait' : 'done') + ')'}
							/>
						{/if}
					{/each}
				</svg>
				{#each g.nodes as n (n.key)}
					{@const r = n.row}
					{@const tone = r ? toneOfItem(r) : 'done'}
					{@const waits = waitingFor(n)}
					{@const holds = holding(n)}
					<button
						class="absolute flex flex-col justify-center gap-0.5 rounded-lg border bg-card px-2.5 text-left shadow-xs transition-all hover:border-foreground/30
							{n.key === focus ? 'ring-2 ring-ring' : ''} {lit && !lit.has(n.key) ? 'opacity-30' : ''} {r ? '' : 'border-dashed bg-card/60'}"
						style="left:{x(n)}px;top:{y(n)}px;width:{W}px;height:{H}px"
						onmouseenter={() => (hover = n.key)}
						onmouseleave={() => (hover = null)}
						onfocus={() => (hover = n.key)}
						onblur={() => (hover = null)}
						onclick={() => openItem(n.project, n.id)}
						title={r?.title || n.id}
					>
						<span class="flex min-w-0 items-center gap-1.5">
							<StatusIcon {tone} spin={r?.activity === 'working'} size={12} />
							<span class="num shrink-0 text-[11px] font-semibold">{n.id}</span>
							{#if !session.scope}<span class="num truncate rounded bg-muted px-1 text-[10px] text-muted-foreground">{n.project}</span>{/if}
							{#if holds}<span class="num ml-auto shrink-0 rounded bg-st-needs/15 px-1 text-[10px] font-medium text-st-needs" title={t('depHoldsTitle', holds)}>{t('depHolds', holds)}</span>{/if}
						</span>
						<span class="truncate text-xs leading-tight">{r?.title || t('depOutside')}</span>
						{#if !compact && r}
							<span class="num truncate text-[10px] {waits ? 'text-st-needs' : TEXT[signalTone(r)]}">{waits ? t('depWaitsN', waits) : signalText(r)}</span>
						{/if}
					</button>
				{/each}
			</div>
		</div>
	</div>
{/if}
