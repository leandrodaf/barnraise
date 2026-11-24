<script lang="ts">
	import BrandMark from '$lib/app/BrandMark.svelte';
	import * as Card from '$lib/components/ui/card';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { errorText } from '$lib/api';
	import { t } from '$lib/i18n';
	import { session } from '$lib/session.svelte';
	import LangSwitch from '$lib/app/LangSwitch.svelte';

	let value = $state('');
	let error = $state(session.signInError ? t(session.signInError) : '');
	let busy = $state(false);

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		const v = value.trim();
		if (!v) return;
		busy = true;
		error = '';
		try {
			if (!(await session.signIn(v))) error = t('tokenBad');
		} catch (err) {
			error = t('error', errorText(err));
		} finally {
			busy = false;
		}
	}
</script>

<div class="relative grid min-h-svh place-items-center bg-muted/40 p-4">
	<div class="absolute top-4 right-4"><LangSwitch compact /></div>
	<Card.Root class="w-full max-w-sm">
		<Card.Header>
			<BrandMark class="mb-2 size-10" />
			<Card.Title class="text-xl">{t('signin')}</Card.Title>
			<Card.Description>{t('tokenHelp')}</Card.Description>
		</Card.Header>
		<Card.Content>
			<form class="grid gap-4" onsubmit={submit}>
				<div class="grid gap-1.5">
					<Label for="token">{t('tokenLabel')}</Label>
					<Input id="token" type="password" autocomplete="current-password" spellcheck={false} required bind:value aria-invalid={error ? true : undefined} />
				</div>
				{#if error}<p class="text-sm text-destructive" role="alert">{error}</p>{/if}
				<Button type="submit" disabled={busy}>{t('signin')}</Button>
			</form>
		</Card.Content>
	</Card.Root>
</div>
