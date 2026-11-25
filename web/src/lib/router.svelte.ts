// Hash routes: #/queue, #/item/nobira/I-7. The item carries its project, so
// opening one never changes the scope picked in the header.
export type RouteName =
	| 'home' | 'overview' | 'queue' | 'item' | 'agents' | 'accounts' | 'proposals' | 'machines' | 'capabilities' | 'parts' | 'metrics' | 'processes' | 'activity' | 'admin';

const NAMES: RouteName[] = ['home', 'overview', 'queue', 'item', 'agents', 'accounts', 'proposals', 'machines', 'capabilities', 'parts', 'metrics', 'processes', 'activity', 'admin'];

type Route = { name: RouteName; args: string[] };

function parse(): Route {
	const parts = (location.hash || '#/home').replace(/^#\/?/, '').split('/').filter(Boolean).map(decodeURIComponent);
	const name = (NAMES as string[]).includes(parts[0]) ? (parts[0] as RouteName) : 'home';
	return { name, args: parts.slice(1) };
}

class Router {
	current = $state(parse());
	constructor() {
		window.addEventListener('hashchange', () => {
			this.current = parse();
			document.getElementById('scroller')?.scrollTo(0, 0);
		});
	}
}

export const router = new Router();
export const href = (name: RouteName, ...args: string[]) => '#/' + [name, ...args.filter(Boolean).map(encodeURIComponent)].join('/');
export const go = (name: RouteName, ...args: string[]) => (location.hash = href(name, ...args));
