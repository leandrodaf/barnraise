// The one conversation panel of the page: which question or item it is about.
class DiscussionPanel {
	open = $state(false);
	project = $state('');
	thread = $state('');
	title = $state('');
	show(project: string, thread: string, title: string) {
		this.project = project;
		this.thread = thread;
		this.title = title;
		this.open = true;
	}
}

export const discussion = new DiscussionPanel();
