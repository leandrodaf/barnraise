<script lang="ts">
	// The pictures an item's work made: a sprite drawn, a screenshot
	// committed, an icon replaced. Each opens large; one that replaced
	// another can be seen before, after or side by side, if wanted.
	import * as Dialog from '$lib/components/ui/dialog';
	import { Button } from '$lib/components/ui/button';
	import { Badge } from '$lib/components/ui/badge';
	import ImageIcon from '@lucide/svelte/icons/image';
	import { images, type ItemImage } from '$lib/rest';
	import { t } from '$lib/i18n';
	import Section from './Section.svelte';

	let { project, id, version = '' }: { project: string; id: string; version?: string } = $props();

	let list = $state<ItemImage[]>([]);
	let more = $state(0);
	let urls = $state<Record<string, string>>({});
	let small = $state<Record<string, boolean>>({});
	let open = $state<ItemImage | null>(null);
	let all = $state(false);
	const FIRST = 12;
	const shown = $derived(all ? list : list.slice(0, FIRST));
	let mode = $state<'after' | 'before' | 'both'>('after');
	let rev = '';

	// Each picture is kept by the shared cache (rest.ts): a second look at
	// the item shows them at once.
	async function load(path: string, before = false) {
		const key = (before ? 'b:' : 'a:') + path;
		if (urls[key]) return;
		try {
			urls[key] = await images.url(project, id, path, before, rev);
		} catch {
			urls[key] = '';
		}
	}
	$effect(() => {
		void version; // a new step may bring new pictures
		images
			.ofItem(project, id)
			.then((v) => {
				rev = v.rev || '';
				list = v.images;
				more = v.more || 0;
				for (const im of v.images.slice(0, FIRST)) load(im.path);
			})
			.catch(() => (list = []));
	});

	// Pixel art is small: shown large, it stays crisp instead of blurred.
	const measure = (path: string) => (e: Event) => {
		const img = e.currentTarget as HTMLImageElement;
		small[path] = img.naturalWidth > 0 && img.naturalWidth <= 256 && img.naturalHeight <= 256;
	};
	function show(im: ItemImage) {
		open = im;
		mode = 'after';
		load(im.path);
		if (!im.added) load(im.path, true);
	}
	const name = (p: string) => p.split('/').pop() || p;
	const dir = (p: string) => p.split('/').slice(0, -1).join('/');
</script>

{#snippet pic(key: string, path: string, label: string)}
	<figure class="flex min-w-0 flex-1 flex-col items-center gap-2">
		<div class="grid min-h-48 w-full place-items-center rounded-lg border bg-[repeating-conic-gradient(var(--muted)_0_25%,transparent_0_50%)] bg-[length:16px_16px] p-3">
			{#if urls[key]}
				<img src={urls[key]} alt={path} class="max-h-[65vh] max-w-full object-contain {small[path] ? 'min-w-48 [image-rendering:pixelated]' : ''}" />
			{:else if urls[key] === ''}
				<p class="text-xs text-muted-foreground">{t('imgNone')}</p>
			{/if}
		</div>
		{#if label}<figcaption class="text-xs text-muted-foreground">{label}</figcaption>{/if}
	</figure>
{/snippet}

{#if list.length}
	<Section title={t('imgTitle')} count={list.length + (more ? '+' : '')} sub={t('imgHelp')}>
		<ul class="grid grid-cols-[repeat(auto-fill,minmax(7.5rem,1fr))] gap-3">
			{#each shown as im (im.path)}
				<li>
					<button class="group w-full text-left" onclick={() => show(im)} title={im.path}>
						<div class="grid aspect-square place-items-center overflow-hidden rounded-lg border bg-[repeating-conic-gradient(var(--muted)_0_25%,transparent_0_50%)] bg-[length:12px_12px] p-2 transition group-hover:border-foreground/40">
							{#if urls['a:' + im.path]}
								<img src={urls['a:' + im.path]} alt={im.path} loading="lazy" onload={measure(im.path)} class="max-h-full max-w-full object-contain {small[im.path] ? 'h-full w-full [image-rendering:pixelated]' : ''}" />
							{:else}<ImageIcon class="size-5 text-muted-foreground/50" />{/if}
						</div>
						<p class="mt-1 truncate text-xs font-medium">{name(im.path)}</p>
						<p class="flex items-center gap-1 truncate text-[11px] text-muted-foreground">
							<Badge variant={im.added ? 'secondary' : 'outline'} class="px-1 py-0 text-[10px]">{im.added ? t('imgNew') : t('imgChanged')}</Badge>
							<span class="truncate">{dir(im.path)}</span>
						</p>
					</button>
				</li>
			{/each}
		</ul>
		{#if list.length > FIRST}
			<Button variant="outline" size="sm" class="mt-3" onclick={() => { all = !all; if (all) for (const im of list) load(im.path); }}>{all ? t('imgFewer') : t('imgAll', list.length + (more ? '+' : ''))}</Button>
		{/if}
	</Section>
{/if}

<Dialog.Root open={!!open} onOpenChange={(v) => !v && (open = null)}>
	<Dialog.Content class="sm:max-w-5xl">
		{#if open}
			<Dialog.Header>
				<Dialog.Title class="truncate font-mono text-sm">{open.path}</Dialog.Title>
				<Dialog.Description>{open.added ? t('imgNewHelp') : t('imgChangedHelp')}</Dialog.Description>
			</Dialog.Header>
			{#if !open.added}
				<div class="flex gap-1">
					{#each [['after', t('imgAfter')], ['before', t('imgBefore')], ['both', t('imgBoth')]] as [v, label] (v)}
						<Button size="sm" variant={mode === v ? 'default' : 'outline'} onclick={() => (mode = v as typeof mode)}>{label}</Button>
					{/each}
				</div>
			{/if}
			<div class="flex flex-col gap-3 sm:flex-row">
				{#if mode === 'both'}
					{@render pic('b:' + open.path, open.path, t('imgBefore'))}
					{@render pic('a:' + open.path, open.path, t('imgAfter'))}
				{:else}
					{@render pic((mode === 'before' ? 'b:' : 'a:') + open.path, open.path, '')}
				{/if}
			</div>
		{/if}
	</Dialog.Content>
</Dialog.Root>
