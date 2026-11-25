// Mermaid is loaded the first time a diagram is shown, so screens without one
// never pay for it. It runs in strict mode: diagram text comes from agents and
// trackers, so labels are sanitized and clicks and scripts are off.
//
// Diagrams wear the panel's own look: its tokens (light or dark) resolved to
// colors, Geist Mono for every label, nodes tinted with the accent of their
// side (slate before the change, green after it).
import { theme } from '$lib/theme.svelte';
import monoFontUrl from '@fontsource-variable/geist-mono/files/geist-mono-latin-wght-normal.woff2?url';

type Mermaid = typeof import('mermaid').default;
export type DiagramTone = '' | 'before' | 'after';

const FONT = 'Geist Mono Variable';
let loading: Promise<Mermaid> | null = null;
let fontFace: Promise<string> | null = null;
let queue: Promise<unknown> = Promise.resolve();
let seq = 0;

// The font travels inside each SVG: an image never reaches the page's fonts.
function embeddedFont(): Promise<string> {
	fontFace ??= fetch(monoFontUrl)
		.then((r) => r.blob())
		.then((b) => new Promise<string>((ok) => {
			const rd = new FileReader();
			rd.onload = () => ok(`@font-face{font-family:'${FONT}';font-weight:100 900;src:url(${rd.result}) format('woff2');}`);
			rd.onerror = () => ok('');
			rd.readAsDataURL(b);
		}))
		.catch(() => '');
	return fontFace;
}

// rgb resolves a CSS color (oklch, with alpha…) to [r, g, b] through a canvas.
let probe: CanvasRenderingContext2D | null = null;
function rgb(css: string): number[] {
	probe ??= document.createElement('canvas').getContext('2d', { willReadFrequently: true })!;
	probe.clearRect(0, 0, 1, 1);
	probe.fillStyle = '#888';
	probe.fillStyle = css;
	probe.fillRect(0, 0, 1, 1);
	const d = probe.getImageData(0, 0, 1, 1).data;
	return [d[0], d[1], d[2]];
}
const token = (name: string) => rgb(getComputedStyle(document.documentElement).getPropertyValue(name).trim() || '#888');
const hex = (c: number[]) => '#' + c.map((v) => Math.round(Math.max(0, Math.min(255, v))).toString(16).padStart(2, '0')).join('');
// mix blends a over b with weight w.
const mix = (a: number[], b: number[], w: number) => [0, 1, 2].map((i) => a[i] * w + b[i] * (1 - w));

function palette(tone: DiagramTone) {
	const dark = theme.current === 'dark';
	const bg = token('--card'), fg = token('--foreground'), muted = token('--muted-foreground');
	const accent = token(tone === 'after' ? '--st-done' : tone === 'before' ? '--st-idle' : '--st-work');
	const review = token('--st-review'), queueC = token('--st-queue'), needs = token('--st-needs');
	const tint = (c: number[]) => mix(c, bg, dark ? 0.15 : 0.08);
	const edge = (c: number[]) => mix(c, bg, dark ? 0.75 : 0.6);
	const node = tint(accent), border = edge(accent);
	const line = mix(fg, bg, dark ? 0.5 : 0.45);
	const text = mix(fg, bg, 0.92);
	const soft = mix(muted, bg, 0.9);
	const panel = mix(fg, bg, dark ? 0.045 : 0.03);
	const faint = mix(fg, bg, 0.2);
	const v = {
		darkMode: dark, background: hex(bg), fontFamily: `'${FONT}', ui-monospace, monospace`, fontSize: '13px',
		primaryColor: hex(node), primaryTextColor: hex(text), primaryBorderColor: hex(border),
		secondaryColor: hex(tint(queueC)), secondaryTextColor: hex(text), secondaryBorderColor: hex(edge(queueC)),
		tertiaryColor: hex(tint(review)), tertiaryTextColor: hex(text), tertiaryBorderColor: hex(edge(review)),
		mainBkg: hex(node), nodeBorder: hex(border), nodeTextColor: hex(text), textColor: hex(text), titleColor: hex(text),
		lineColor: hex(line), defaultLinkColor: hex(line),
		clusterBkg: hex(panel), clusterBorder: hex(faint), edgeLabelBackground: hex(bg),
		actorBkg: hex(node), actorBorder: hex(border), actorTextColor: hex(text), actorLineColor: hex(faint),
		signalColor: hex(line), signalTextColor: hex(text), sequenceNumberColor: hex(bg),
		labelBoxBkgColor: hex(panel), labelBoxBorderColor: hex(mix(fg, bg, 0.3)), labelTextColor: hex(text), loopTextColor: hex(soft),
		noteBkgColor: hex(tint(needs)), noteBorderColor: hex(edge(needs)), noteTextColor: hex(text),
		activationBkgColor: hex(mix(accent, bg, 0.3)), activationBorderColor: hex(border),
		classText: hex(text), labelColor: hex(text), altBackground: hex(panel),
		stateBkg: hex(node), stateBorder: hex(border), transitionColor: hex(line), compositeBackground: hex(panel), specialStateColor: hex(accent),
		pie1: hex(accent), pie2: hex(queueC), pie3: hex(review), pie4: hex(needs), pieStrokeColor: hex(bg), pieOuterStrokeColor: hex(border),
		git0: hex(accent), git1: hex(queueC), git2: hex(review), git3: hex(needs)
	};
	const glow = hex(accent);
	const css = `
		text, .label, .nodeLabel, .edgeLabel, .messageText, .actor, .loopText, .noteText, .labelText, .classTitle, .cluster-label {
			font-family: '${FONT}', ui-monospace, monospace !important; letter-spacing: 0.01em; }
		.node rect, .node polygon, .node circle, rect.actor, .basic.label-container, .statediagram-state rect, .classGroup rect {
			rx: 7px; ry: 7px; stroke-width: 1.25px; }
		.node rect, .node polygon, rect.actor, .statediagram-state rect { filter: drop-shadow(0 0 7px ${glow}${dark ? '33' : '22'}); }
		.flowchart-link, .messageLine0, .messageLine1, .relation, .transition { stroke-width: 1.4px; }
		.messageLine1 { stroke-dasharray: 4 3; }
		.cluster rect, .loopLine { stroke-dasharray: 4 3; }
		.loopLine { stroke-width: 1.1px; }
		.labelBox { rx: 4px; ry: 4px; }
		.labelText, .labelText > tspan { font-weight: 600; font-size: 11px; }
		.edgeLabel rect, .labelBkg { fill: ${hex(bg)}; }
		.note { rx: 6px; ry: 6px; }
		.actor-line { stroke-dasharray: 2 4; }
	`;
	return { v, css };
}

async function load(): Promise<Mermaid> {
	loading ??= Promise.all([import('mermaid'), document.fonts?.load(`13px '${FONT}'`).catch(() => null)]).then(([m]) => m.default);
	return loading;
}

// renderMermaid returns the SVG of a diagram in the panel's look, or throws
// with Mermaid's message. Renders take turns: each sets its own theme.
export function renderMermaid(source: string, tone: DiagramTone = ''): Promise<string> {
	const job = queue.then(async () => {
		const [m, font] = await Promise.all([load(), embeddedFont()]);
		const p = palette(tone);
		m.initialize({
			startOnLoad: false,
			securityLevel: 'strict',
			theme: 'base',
			themeVariables: p.v,
			themeCSS: p.css,
			fontFamily: p.v.fontFamily,
			flowchart: { htmlLabels: false, curve: 'basis', padding: 14, nodeSpacing: 46, rankSpacing: 52 },
			sequence: { mirrorActors: false, actorMargin: 56, boxMargin: 8, messageMargin: 38, noteMargin: 12 },
			suppressErrorRendering: true
		});
		const id = 'mmd-' + ++seq;
		try {
			const { svg } = await m.render(id, source);
			return font ? svg.replace(/<svg\b[^>]*>/, (open) => open + `<style>${font}</style>`) : svg;
		} finally {
			document.getElementById(id)?.remove();
			document.getElementById('d' + id)?.remove();
		}
	});
	queue = job.catch(() => null);
	return job;
}

// kindOf names a diagram's type from its first line, for the caption.
export function kindOf(source: string): string {
	const first = source.split('\n').map((l) => l.trim()).find((l) => l && !l.startsWith('%%') && l !== '---') || '';
	const k = first.split(/\s+/)[0].toLowerCase();
	const names: Record<string, string> = { graph: 'flow', flowchart: 'flow', sequencediagram: 'sequence', classdiagram: 'class', statediagram: 'state',
		'statediagram-v2': 'state', erdiagram: 'er', gantt: 'gantt', pie: 'pie', gitgraph: 'git', mindmap: 'mindmap', timeline: 'timeline', c4context: 'c4', architecture: 'architecture' };
	return names[k] || k;
}
