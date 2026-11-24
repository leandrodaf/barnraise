// A small Markdown reader for item and tracker text. It returns blocks and
// inline pieces that Svelte renders as text: no HTML is ever parsed or
// inserted, so a description cannot inject anything into the page.
export type Inline = { t: 'text' | 'bold' | 'code' | 'em'; v: string };
export type Block =
	| { k: 'h'; level: number; parts: Inline[] }
	| { k: 'p'; parts: Inline[] }
	| { k: 'li'; parts: Inline[]; check?: boolean; done?: boolean; ordered?: string; depth: number }
	| { k: 'quote'; parts: Inline[] }
	| { k: 'pre'; v: string }
	| { k: 'hr' };

export function inline(s: string): Inline[] {
	const out: Inline[] = [];
	const re = /(\*\*[^*]+\*\*|`[^`]+`|(?<![\w*])\*[^*\s][^*]*\*(?!\w)|_[^_\s][^_]*_)/g;
	let last = 0;
	for (const m of s.matchAll(re)) {
		if (m.index! > last) out.push({ t: 'text', v: s.slice(last, m.index) });
		const x = m[0];
		if (x.startsWith('**')) out.push({ t: 'bold', v: x.slice(2, -2) });
		else if (x.startsWith('`')) out.push({ t: 'code', v: x.slice(1, -1) });
		else out.push({ t: 'em', v: x.slice(1, -1) });
		last = m.index! + x.length;
	}
	if (last < s.length) out.push({ t: 'text', v: s.slice(last) });
	return out;
}

export function parse(src: string): Block[] {
	const lines = src.replace(/\r/g, '').split('\n');
	const out: Block[] = [];
	let para: string[] = [];
	const flush = () => {
		if (para.length) out.push({ k: 'p', parts: inline(para.join(' ')) });
		para = [];
	};
	for (let i = 0; i < lines.length; i++) {
		const l = lines[i];
		if (/^\s*```/.test(l)) {
			flush();
			const body: string[] = [];
			while (++i < lines.length && !/^\s*```/.test(lines[i])) body.push(lines[i]);
			out.push({ k: 'pre', v: body.join('\n') });
			continue;
		}
		let m: RegExpMatchArray | null;
		if (!l.trim()) flush();
		else if ((m = l.match(/^(#{1,6})\s+(.*)$/))) { flush(); out.push({ k: 'h', level: m[1].length, parts: inline(m[2]) }); }
		else if (/^\s*(?:-\s*){3,}$|^\s*(?:\*\s*){3,}$|^\s*(?:_\s*){3,}$/.test(l)) { flush(); out.push({ k: 'hr' }); }
		else if ((m = l.match(/^(\s*)[-*+]\s+\[( |x|X)\]\s+(.*)$/))) { flush(); out.push({ k: 'li', check: true, done: m[2] !== ' ', parts: inline(m[3]), depth: Math.floor(m[1].length / 2) }); }
		else if ((m = l.match(/^(\s*)[-*+]\s+(.*)$/))) { flush(); out.push({ k: 'li', parts: inline(m[2]), depth: Math.floor(m[1].length / 2) }); }
		else if ((m = l.match(/^(\s*)(\d+)[.)]\s+(.*)$/))) { flush(); out.push({ k: 'li', ordered: m[2], parts: inline(m[3]), depth: Math.floor(m[1].length / 2) }); }
		else if ((m = l.match(/^>\s?(.*)$/))) { flush(); out.push({ k: 'quote', parts: inline(m[1]) }); }
		else para.push(l.trim());
	}
	flush();
	return out;
}
