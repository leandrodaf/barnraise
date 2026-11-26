// The desktop app shows the panel in a webview that opens no new window: a
// link meant for a browser tab (the pull request, the commit, the issue)
// did nothing there. Inside the app such a link goes to the app instead,
// which opens it in the system's browser ("BO:" is Wails' BrowserOpenURL
// message). In a browser nothing changes.

type Handler = { postMessage: (message: string) => void };

// appChannel is the desktop app's message channel, or null in a browser.
function appChannel(): Handler | null {
	const w = window as unknown as {
		webkit?: { messageHandlers?: { external?: Handler } };
		chrome?: { webview?: Handler };
	};
	// WebKitGTK (Linux) and WKWebView (macOS), then WebView2 (Windows).
	return w.webkit?.messageHandlers?.external ?? w.chrome?.webview ?? null;
}

const external = (href: string) => {
	try {
		const u = new URL(href, location.href);
		return (u.protocol === 'http:' || u.protocol === 'https:' || u.protocol === 'mailto:') && u.origin !== location.origin;
	} catch {
		return false;
	}
};

// openOutside opens url in the system's browser from the app, or in a new
// tab from a browser.
export function openOutside(url: string) {
	const app = appChannel();
	if (app) app.postMessage('BO:' + new URL(url, location.href).href);
	else window.open(url, '_blank', 'noopener,noreferrer');
}

// routeExternalLinks sends every link that leaves the panel to the system's
// browser when the panel runs inside the desktop app.
export function routeExternalLinks() {
	if (!appChannel()) return;
	document.addEventListener(
		'click',
		(e) => {
			if (e.defaultPrevented || e.button !== 0) return;
			const a = (e.target as Element | null)?.closest?.('a[href]') as HTMLAnchorElement | null;
			if (!a || !(a.target === '_blank' || external(a.href))) return;
			e.preventDefault();
			openOutside(a.href);
		},
		true
	);
	const open = window.open.bind(window);
	window.open = (url?: string | URL, target?: string, features?: string) => {
		if (url && external(String(url))) {
			openOutside(String(url));
			return null;
		}
		return open(url, target, features);
	};
}
