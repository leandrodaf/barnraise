// Light, dark or the system's choice, kept in this browser. No script runs in
// the page head (the daemon's CSP forbids inline scripts), so the class is set
// as the bundle starts.
import { store } from './api';

export type ThemeChoice = 'system' | 'light' | 'dark';

const media = window.matchMedia('(prefers-color-scheme: dark)');

class Theme {
	choice = $state<ThemeChoice>((store.get('theme') as ThemeChoice) || 'system');
	system = $state<'light' | 'dark'>(media.matches ? 'dark' : 'light');
	get current(): 'light' | 'dark' {
		return this.choice === 'system' ? this.system : this.choice;
	}
	constructor() {
		media.addEventListener('change', (e) => {
			this.system = e.matches ? 'dark' : 'light';
			this.apply();
		});
		this.apply();
	}
	set(c: ThemeChoice) {
		this.choice = c;
		store.set('theme', c === 'system' ? '' : c);
		this.apply();
	}
	cycle() {
		this.set(this.choice === 'system' ? 'light' : this.choice === 'light' ? 'dark' : 'system');
	}
	apply() {
		document.documentElement.classList.toggle('dark', this.current === 'dark');
		document.documentElement.style.colorScheme = this.current;
	}
}

export const theme = new Theme();
