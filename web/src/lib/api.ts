// The panel's only way to the daemon: the public API (Connect, JSON) on the
// same origin that served the page, with the owner's token. The desktop app
// opens this same page from the daemon, so there is one path for both.
import { Code, ConnectError, createClient, type Interceptor } from '@connectrpc/connect';
import { createConnectTransport } from '@connectrpc/connect-web';
import { BarnraiseService } from './gen/barnraise/v1/barnraise_pb';

const KEY = 'barnraise.';
export const store = {
	get(k: string): string {
		try {
			return localStorage.getItem(KEY + k) || '';
		} catch {
			return '';
		}
	},
	set(k: string, v: string) {
		try {
			if (v) localStorage.setItem(KEY + k, v);
			else localStorage.removeItem(KEY + k);
		} catch {
			/* private mode */
		}
	}
};

let token = '';
export const getToken = () => token;
export function setToken(v: string) {
	token = v;
	store.set('token', v);
}

const auth: Interceptor = (next) => async (req) => {
	if (token) req.header.set('Authorization', 'Bearer ' + token);
	return next(req);
};

export const api = createClient(
	BarnraiseService,
	createConnectTransport({ baseUrl: location.origin, interceptors: [auth], useBinaryFormat: false })
);

export const isAuthError = (e: unknown) =>
	e instanceof ConnectError && (e.code === Code.Unauthenticated || e.code === Code.PermissionDenied);

export function errorText(e: unknown): string {
	if (e instanceof ConnectError) return e.rawMessage || e.message;
	return e instanceof Error ? e.message : String(e);
}

// takeTokenFromURL reads ?token= once (the CLI and the desktop app open the
// panel that way) and removes it from the address bar and the history.
export function takeTokenFromURL(): string {
	const url = new URL(location.href);
	const v = url.searchParams.get('token') || '';
	if (v) {
		url.searchParams.delete('token');
		history.replaceState(null, '', url.pathname + url.search + url.hash);
	}
	return v;
}
