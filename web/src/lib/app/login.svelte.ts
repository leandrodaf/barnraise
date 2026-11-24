// The one login panel of the page: which account is being logged in on the
// machines. An empty account is a new one, of the provider picked there.
class LoginPanel {
	open = $state(false);
	account = $state('');
	provider = $state('');
	show(account = '', provider = '') {
		this.account = account;
		this.provider = provider;
		this.open = true;
	}
}

export const loginPanel = new LoginPanel();
