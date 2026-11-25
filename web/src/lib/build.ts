// The panel loads some code only when it is needed (Mermaid, for one), from
// files named after their content. A page opened before the daemon was
// updated still asks for the files of the build it came with, and the new
// daemon no longer has them: the import fails ("Importing a module script
// failed." in WebKit, so in the desktop app). The page stays open for days
// (the event stream reconnects on its own), so this is the usual case.
//
// When a lazy import fails, the page asks the daemon for its index.html: if
// it starts another build than this page's, the page reloads on the current
// one. The same build means the file is truly missing, and the error stands.

const entryOf = (html: string, base: string) => {
	const m = html.match(/<script\b[^>]*\btype="module"[^>]*\bsrc="([^"]+)"/) || html.match(/<script\b[^>]*\bsrc="([^"]+)"[^>]*\btype="module"/);
	return m ? new URL(m[1], base).href : '';
};

// thisBuild is the entry script this page runs.
function thisBuild(): string {
	const s = document.querySelector<HTMLScriptElement>('script[type="module"][src]');
	return s ? s.src : '';
}

// servedBuild is the entry script the daemon serves now ('' when unknown).
async function servedBuild(): Promise<string> {
	const url = new URL('./', location.href);
	url.search = '';
	url.hash = '';
	try {
		const r = await fetch(url, { cache: 'no-store', credentials: 'same-origin' });
		return r.ok ? entryOf(await r.text(), url.href) : '';
	} catch {
		return '';
	}
}

let checking: Promise<boolean> | null = null;

// reloadIfRebuilt reloads the page when the daemon serves another build than
// this page's, and says whether it did.
export function reloadIfRebuilt(): Promise<boolean> {
	checking ??= (async () => {
		const mine = thisBuild();
		const served = await servedBuild();
		checking = null;
		if (!mine || !served || mine === served) return false;
		location.reload();
		return true;
	})();
	return checking;
}

// watchStaleBuild reloads on the current build when a lazy import of this one
// fails (Vite reports it as vite:preloadError).
export function watchStaleBuild() {
	window.addEventListener('vite:preloadError', () => void reloadIfRebuilt());
}
