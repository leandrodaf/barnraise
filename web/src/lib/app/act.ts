// act runs an owner's action: the result or the error as a toast, then every
// screen reads again.
import { toast } from 'svelte-sonner';
import { errorText } from '$lib/api';
import { t } from '$lib/i18n';
import { session } from '$lib/session.svelte';

export async function act<T>(fn: () => Promise<T>, ok?: string | ((r: T) => string)): Promise<T | null> {
	try {
		const r = await fn();
		const msg = typeof ok === 'function' ? ok(r) : ok;
		if (msg) toast.success(msg);
		session.bump(0);
		return r;
	} catch (e) {
		if (!session.failed(e)) toast.error(t('error', errorText(e)));
		return null;
	}
}
