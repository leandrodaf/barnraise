import { go } from '$lib/router.svelte';

// openItem goes to an item of any project; the scope stays as it is.
export function openItem(project: string, id: string) {
	go('item', project, id);
}
