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
