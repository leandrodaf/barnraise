// The one conversation panel of the page: which question, item or proposal
// it is about.
class DiscussionPanel {
	open = $state(false);
	project = $state('');
	thread = $state('');
	title = $state('');
	// The owner asked, from a screen, for the advisor to decide: the panel
	// opens and starts it, so the owner follows what is decided.
	delegate = $state(false);
	show(project: string, thread: string, title: string, delegate = false) {
		this.project = project;
		this.thread = thread;
		this.title = title;
		this.delegate = delegate;
		this.open = true;
	}
}

export const discussion = new DiscussionPanel();

// The advisor's word on the open proposals, kept while the page is open: a
// triage costs an answer of the strongest model, so leaving the screen does
// not lose it. Keyed by project and proposal.
export type Verdict = { project: string; id: string; decision: string; reason: string; skip: boolean };
class Triage {
	verdicts = $state<Record<string, Verdict>>({});
	busy = $state(false);
	started = $state(0);
	get list() {
		return Object.values(this.verdicts);
	}
	clear() {
		this.verdicts = {};
	}
}
export const triage = new Triage();
export const triageKey = (project: string, id: string) => project + '/' + id;
