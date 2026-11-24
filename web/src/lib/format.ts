// Numbers, sizes, times and durations in the reader's language.
import { locale } from './locale.svelte';

// Formatters of the current language, made once per language.
const cache = new Map<string, ReturnType<typeof make>>();
function make(l: string) {
	return {
		nf: new Intl.NumberFormat(l, { maximumFractionDigits: 1 }),
		nfc: new Intl.NumberFormat(l, { notation: 'compact', maximumFractionDigits: 1 }),
		usd: new Intl.NumberFormat(l, { style: 'currency', currency: 'USD', maximumFractionDigits: 2 }),
		rtf: new Intl.RelativeTimeFormat(l, { numeric: 'auto' }),
		dtf: new Intl.DateTimeFormat(l, { dateStyle: 'medium', timeStyle: 'short' }),
		df: new Intl.DateTimeFormat(l, { dateStyle: 'medium' })
	};
}
function f() {
	const l = locale.lang;
	let x = cache.get(l);
	if (!x) cache.set(l, (x = make(l)));
	return x;
}

type N = number | bigint | undefined | null;
const n = (v: N) => Number(v ?? 0) || 0;

export const num = (v: N) => f().nf.format(n(v));
export const compact = (v: N) => f().nfc.format(n(v));
export const money = (v: N) => f().usd.format(n(v));
export const pct = (v: N) => f().nf.format(n(v)) + '%';
// ratioPct takes 0..1 or 0..100.
export const ratioPct = (v: N) => pct(n(v) <= 1 ? n(v) * 100 : n(v));

export function bytes(v: N): string {
	let b = n(v);
	if (b <= 0) return '0 B';
	const units = ['B', 'KB', 'MB', 'GB', 'TB'];
	let i = 0;
	while (b >= 1000 && i < units.length - 1) {
		b /= 1000;
		i++;
	}
	return f().nf.format(b) + ' ' + units[i];
}

export function parseTime(s?: string): Date | null {
	if (!s || s.startsWith('0001-')) return null;
	const d = new Date(s);
	return isNaN(d.getTime()) ? null : d;
}

export function ago(s?: string): string {
	const d = parseTime(s);
	if (!d) return '';
	const sec = (d.getTime() - Date.now()) / 1000;
	const a = Math.abs(sec);
	if (a < 60) return f().rtf.format(Math.round(sec), 'second');
	if (a < 3600) return f().rtf.format(Math.round(sec / 60), 'minute');
	if (a < 86400) return f().rtf.format(Math.round(sec / 3600), 'hour');
	return f().rtf.format(Math.round(sec / 86400), 'day');
}

export const when = (s?: string) => {
	const d = parseTime(s);
	return d ? f().dtf.format(d) : '';
};
export const day = (s?: string) => {
	const d = parseTime(s);
	return d ? f().df.format(d) : s || '';
};

export function dur(sec: N): string {
	let s = Math.max(0, Math.round(n(sec)));
	if (s < 60) return s + 's';
	const m = Math.floor(s / 60);
	if (m < 60) return m + 'm';
	const h = Math.floor(m / 60);
	if (h < 48) return h + 'h ' + (m % 60) + 'm';
	return Math.floor(h / 24) + 'd ' + (h % 24) + 'h';
}

export const since = (s?: string) => {
	const d = parseTime(s);
	return d ? dur((Date.now() - d.getTime()) / 1000) : '';
};
