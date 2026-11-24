// The panel's language: English by default, Brazilian Portuguese on choice,
// kept in this browser. Reading it inside t() makes every text on screen
// follow a switch at once, without a reload.
export type Lang = 'en' | 'pt-BR';

const saved = (() => {
	try {
		return localStorage.getItem('barnraise.lang');
	} catch {
		return null;
	}
})();

export const locale = $state({ lang: (saved === 'pt-BR' ? 'pt-BR' : 'en') as Lang });

export function setLang(l: Lang) {
	locale.lang = l;
	try {
		localStorage.setItem('barnraise.lang', l);
	} catch {
		/* private mode */
	}
	document.documentElement.lang = l;
}

document.documentElement.lang = locale.lang;
