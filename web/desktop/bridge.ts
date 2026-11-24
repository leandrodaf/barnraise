// The Go side of the desktop app (desktop/app.go), bound by Wails.
export type Local = {
	running: boolean;
	url: string;
	machine: string;
	version: string;
	binary: string;
	service: boolean;
	has_token: boolean;
	problem?: string;
	panel_url?: string;
	dev_server: boolean;
	app_version: string;
};

type GoApp = {
	Status(): Promise<Local>;
	StartDaemon(): Promise<Local>;
	Remote(base: string, token: string): Promise<string>;
};

const app = (): GoApp => (window as any).go?.main?.App;
export const inDesktop = () => !!app();
export const status = () => app().Status();
export const startDaemon = () => app().StartDaemon();
export const remote = (base: string, token: string) => app().Remote(base, token);
