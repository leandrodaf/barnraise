// The plain JSON routes next to the Connect API (documents, lessons): the same
// origin and token. A refused token becomes a Connect error, so the session
// treats it like any other call.
import { Code, ConnectError } from '@connectrpc/connect';
import { getToken } from './api';

export async function rest<T>(method: string, path: string, query: Record<string, string> = {}, body?: unknown): Promise<T> {
	const q = new URLSearchParams(Object.entries(query).filter(([, v]) => v !== '' && v !== undefined));
	const url = path + (q.size ? '?' + q : '');
	const headers: Record<string, string> = {};
	const tok = getToken();
	if (tok) headers.Authorization = 'Bearer ' + tok;
	if (body !== undefined) headers['Content-Type'] = 'application/json';
	const r = await fetch(url, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
	const data = await r.json().catch(() => ({}));
	if (!r.ok) {
		const msg = (data && data.error) || r.statusText;
		const code = r.status === 401 ? Code.Unauthenticated : r.status === 403 ? Code.PermissionDenied : r.status === 404 ? Code.NotFound : r.status === 400 ? Code.InvalidArgument : Code.Internal;
		throw new ConnectError(msg, code);
	}
	return data as T;
}

export type Diagram = { title: string; source: string };
export type DocumentFile = { variant: string; path: string; text: string };
export type DocumentView = { name: string; built_in: string; files: DocumentFile[] };
export type DocumentsView = { documents: DocumentView[]; diagrams: string; areas: string[]; kinds: string[]; dir: string };
export type ItemDocuments = { plan: string; diagrams: Diagram[]; pr_body: string; template?: string; warning?: string };
export type DocumentPreview = { text: string; error?: string };

export const documents = {
	list: (project: string) => rest<DocumentsView>('GET', '/v1/documents', { project }),
	save: (project: string, name: string, variant: string, text: string) =>
		rest<DocumentFile>('PUT', '/v1/documents/' + encodeURIComponent(name), { project, variant }, { text }),
	remove: (project: string, name: string, variant: string) => rest<{ ok: boolean }>('DELETE', '/v1/documents/' + encodeURIComponent(name), { project, variant }),
	preview: (project: string, name: string, text: string, area = '', kind = '') =>
		rest<DocumentPreview>('POST', '/v1/documents/' + encodeURIComponent(name) + '/preview', { project }, { text, area, kind }),
	ofItem: (project: string, id: string) => rest<ItemDocuments>('GET', '/v1/items/' + encodeURIComponent(id) + '/documents', { project })
};

export type WorkCommit = { sha: string; subject: string; author?: string; at?: string; files: number; url?: string; step?: string };
export type WorkStep = { step: string; agent: string; status: string; commit?: string; url?: string; at?: string };
export type ItemWork = {
	branch?: string;
	branch_url?: string;
	compare?: string;
	pr?: { number: number; url?: string; state: string };
	commits: WorkCommit[];
	more?: boolean;
	steps: WorkStep[];
	merged?: string;
	merged_url?: string;
	warning?: string;
};

// The proof of an item's work: its commits, each delivery's commit and its pull request.
export const work = {
	ofItem: (project: string, id: string) => rest<ItemWork>('GET', '/v1/items/' + encodeURIComponent(id) + '/work', { project })
};

// The owner's conversation: one session per machine (GET /v1/conversation).
export type ConversationEntry = { seq: number; role: 'user' | 'assistant' | 'tool'; text?: string; tool?: string; input?: string; result?: string; failed?: boolean; at?: string };
export type ConversationAccount = { id: string; provider: string; email?: string; name?: string; plan?: string; week_pct: number; five_hour_pct: number; usable: boolean; why?: string };
export type Conversation = {
	session_id: string; name: string; keep: boolean; available: boolean; why?: string;
	account: string; accounts: ConversationAccount[];
	status: { running: boolean; busy: boolean; provider?: string; remote_url?: string; remote?: string; draft?: string; started_at?: string; error?: string };
	entries: ConversationEntry[];
};
export const conversation = {
	get: (after = 0) => rest<Conversation>('GET', '/v1/conversation', { after: String(after) }),
	send: (text: string) => rest<{ ok: boolean }>('POST', '/v1/conversation/messages', {}, { text }),
	pair: () => rest<{ code: string; manual: string; expires: string }>('POST', '/v1/conversation/pair', {}, {}),
	use: (account: string) => rest<{ ok: boolean }>('POST', '/v1/conversation/account', {}, { account }),
	act: (action: 'start' | 'stop' | 'interrupt' | 'new' | 'keep' | 'release') => rest<{ ok: boolean }>('POST', '/v1/conversation/' + action, {}, {})
};

export type ItemImage = { path: string; added: boolean };
export type ItemImagesView = { images: ItemImage[]; more?: number; note?: string };

// The pictures an item's work added or changed. Each one is fetched with the
// token and shown from an object URL: an <img> cannot send the token.
export const images = {
	ofItem: (project: string, id: string) => rest<ItemImagesView>('GET', '/v1/items/' + encodeURIComponent(id) + '/images', { project }),
	async url(project: string, id: string, path: string, before = false): Promise<string> {
		const q = new URLSearchParams({ project, path, v: before ? 'before' : 'after' });
		const headers: Record<string, string> = {};
		const tok = getToken();
		if (tok) headers.Authorization = 'Bearer ' + tok;
		const r = await fetch('/v1/items/' + encodeURIComponent(id) + '/image?' + q, { headers });
		if (!r.ok) throw new Error((await r.json().catch(() => ({})))?.error || r.statusText);
		return URL.createObjectURL(await r.blob());
	}
};
