// Keyboard: ⌘K / Ctrl+K opens the palette, "g" then a letter goes to a screen,
// "?" lists the shortcuts. Nothing fires while typing in a field.
import { go, type RouteName } from '$lib/router.svelte';

export const GO_KEYS: [string, RouteName][] = [
	['h', 'home'], ['c', 'overview'], ['q', 'queue'], ['r', 'parts'], ['a', 'agents'], ['p', 'proposals'], ['m', 'machines'], ['t', 'accounts'], ['x', 'metrics'], ['o', 'processes'], ['e', 'activity'], ['s', 'admin']
];

class Keys {
	palette = $state(false);
	help = $state(false);
	#g = 0;
	constructor() {
		window.addEventListener('keydown', (e) => {
			if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
				e.preventDefault();
				this.palette = !this.palette;
				return;
			}
			const el = e.target as HTMLElement;
			if (e.metaKey || e.ctrlKey || e.altKey || el.closest('input, textarea, select, [contenteditable], [role=dialog]')) return;
			if (this.#g && Date.now() - this.#g < 1200) {
				this.#g = 0;
				const hit = GO_KEYS.find(([k]) => k === e.key);
				if (hit) {
					e.preventDefault();
					go(hit[1]);
				}
				return;
			}
			if (e.key === 'g') this.#g = Date.now();
			else if (e.key === '?') this.help = true;
			else if (e.key === '/') {
				e.preventDefault();
				this.palette = true;
			}
		});
	}
}

export const keys = new Keys();
