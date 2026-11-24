// One form dialog for the whole page, opened by the screens: the panel never
// uses the browser's own dialogs.
export type Field = {
	name: string;
	label: string;
	type?: 'text' | 'textarea' | 'select';
	required?: boolean;
	value?: string;
	options?: [string, string][];
	help?: string;
	placeholder?: string;
	rows?: number;
	list?: string[];
};

export type FormSpec = {
	title: string;
	description?: string;
	fields: Field[];
	submit: string;
	danger?: boolean;
	// onSubmit returns the message for the toast; throwing shows the error in the form.
	onSubmit: (values: Record<string, string>) => Promise<string | void>;
};

class Dialog {
	spec = $state<FormSpec | null>(null);
	open(spec: FormSpec) {
		this.spec = spec;
	}
	close() {
		this.spec = null;
	}
}

export const dialog = new Dialog();
