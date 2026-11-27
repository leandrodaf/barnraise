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
	can_install: boolean;
	host?: string; // "wsl" on Windows: the daemon lives in WSL
	distro?: string;
	needs_wsl: boolean;
	app_version: string;
	platform: string;
};

type GoApp = {
	Status(): Promise<Local>;
	StartDaemon(): Promise<Local>;
	Install(): Promise<Local>;
	InstallWSL(): Promise<void>;
	Remote(base: string, token: string): Promise<string>;
};

const app = (): GoApp => (window as any).go?.main?.App;
export const inDesktop = () => !!app();
export const status = () => app().Status();
export const startDaemon = () => app().StartDaemon();
export const install = () => app().Install();
export const installWSL = () => app().InstallWSL();
export const remote = (base: string, token: string) => app().Remote(base, token);
