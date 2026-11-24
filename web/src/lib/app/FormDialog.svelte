<script lang="ts">
	import * as Dialog from '$lib/components/ui/dialog';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Textarea } from '$lib/components/ui/textarea';
	import { Label } from '$lib/components/ui/label';
	import * as Select from '$lib/components/ui/select';
	import { toast } from 'svelte-sonner';
	import { errorText } from '$lib/api';
	import { t } from '$lib/i18n';
	import { session } from '$lib/session.svelte';
	import { dialog } from './dialog.svelte';

	let values = $state<Record<string, string>>({});
	let invalid = $state<Record<string, boolean>>({});
	let error = $state('');
	let busy = $state(false);

	$effect(() => {
		const s = dialog.spec;
		if (!s) return;
		values = Object.fromEntries(s.fields.map((f) => [f.name, f.value || '']));
		invalid = {};
		error = '';
	});

	async function submit(ev: SubmitEvent) {
		ev.preventDefault();
		const s = dialog.spec;
		if (!s) return;
		const v = Object.fromEntries(Object.entries(values).map(([k, x]) => [k, x.trim()]));
		invalid = Object.fromEntries(s.fields.filter((f) => f.required && !v[f.name]).map((f) => [f.name, true]));
		if (Object.keys(invalid).length) return;
		busy = true;
		error = '';
		try {
			const msg = await s.onSubmit(v);
			dialog.close();
			if (msg) toast.success(msg);
			session.bump(0);
		} catch (e) {
			if (session.failed(e)) return dialog.close();
			error = t('error', errorText(e));
		} finally {
			busy = false;
		}
	}
</script>

<Dialog.Root open={!!dialog.spec} onOpenChange={(o) => !o && dialog.close()}>
	<Dialog.Content class="sm:max-w-lg">
		{#if dialog.spec}
			{@const s = dialog.spec}
			<form class="grid gap-4" onsubmit={submit} novalidate>
				<Dialog.Header>
					<Dialog.Title>{s.title}</Dialog.Title>
					{#if s.description}<Dialog.Description>{s.description}</Dialog.Description>{/if}
				</Dialog.Header>
				{#each s.fields as f (f.name)}
					<div class="grid gap-1.5">
						<Label for={'f-' + f.name}>{f.label}</Label>
						{#if f.type === 'textarea'}
							<Textarea id={'f-' + f.name} rows={f.rows || 3} placeholder={f.placeholder} bind:value={values[f.name]} aria-invalid={invalid[f.name] || undefined} />
						{:else if f.type === 'select'}
							<Select.Root type="single" bind:value={values[f.name]}>
								<Select.Trigger id={'f-' + f.name} class="w-full">{f.options?.find(([v]) => v === values[f.name])?.[1] || values[f.name]}</Select.Trigger>
								<Select.Content>
									{#each f.options || [] as [v, l] (v)}<Select.Item value={v} label={l} />{/each}
								</Select.Content>
							</Select.Root>
						{:else}
							<Input id={'f-' + f.name} autocomplete="off" placeholder={f.placeholder} list={f.list ? 'f-' + f.name + '-list' : undefined} bind:value={values[f.name]} aria-invalid={invalid[f.name] || undefined} />
							{#if f.list}<datalist id={'f-' + f.name + '-list'}>{#each f.list as o (o)}<option value={o}></option>{/each}</datalist>{/if}
						{/if}
						{#if f.help}<p class="text-xs text-muted-foreground">{f.help}</p>{/if}
					</div>
				{/each}
				{#if error}<p class="text-sm text-destructive" role="alert">{error}</p>{/if}
				<Dialog.Footer>
					<Button type="button" variant="ghost" onclick={() => dialog.close()}>{t('cancel')}</Button>
					<Button type="submit" variant={s.danger ? 'destructive' : 'default'} disabled={busy}>{s.submit}</Button>
				</Dialog.Footer>
			</form>
		{/if}
	</Dialog.Content>
</Dialog.Root>
