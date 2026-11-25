<script lang="ts">
	import * as Tabs from '$lib/components/ui/tabs';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import RadioIcon from '@lucide/svelte/icons/radio';
	import ScrollTextIcon from '@lucide/svelte/icons/scroll-text';
	import ShieldIcon from '@lucide/svelte/icons/shield-check';
	import NewspaperIcon from '@lucide/svelte/icons/newspaper';
	import ChevronDownIcon from '@lucide/svelte/icons/chevron-down';
	import CopyIcon from '@lucide/svelte/icons/copy';
	import SearchIcon from '@lucide/svelte/icons/search';
	import { toast } from 'svelte-sonner';
	import { api } from '$lib/api';
	import type { Event, AuditEntry } from '$lib/gen/barnraise/v1/barnraise_pb';
	import { t, tm } from '$lib/i18n';
	import { ago, when } from '$lib/format';
	import { session } from '$lib/session.svelte';
	import { loader } from '$lib/loader.svelte';
	import { openItem } from '$lib/app/open';
	import type { Tone } from '$lib/status';
	import PageHeader from '$lib/app/PageHeader.svelte';
	import Section from '$lib/app/Section.svelte';
	import Loading from '$lib/app/Loading.svelte';
	import Empty from '$lib/app/Empty.svelte';
	import Dot from '$lib/app/Dot.svelte';
	import Markdown from '$lib/app/Markdown.svelte';

	// What happened in the projects of the scope, newest first: the daemon's
	// events (live), the owner's and tokens' actions (audit) and the digest.
	let tab = $state('events');
	let group = $state('');
	let q = $state('');
	let open = $state<Record<string, boolean>>({});
	const short = (type: string) => type.replace(/^dev\.barnraise\./, '');
	const GROUPS = ['item', 'delivery', 'review', 'check', 'owner', 'agent', 'account', 'machine', 'proposal', 'policy', 'autopilot'];

	const ev = loader(async (projects) => {
		const got = await Promise.all(projects.map((project) => api.listEvents({ project, limit: 400 }).then((r) => r.events).catch(() => [] as Event[])));
		return got.flat().sort((a, b) => (b.time || '').localeCompare(a.time || '')).slice(0, 600);
	});
	const audit = loader(async (projects) => {
		const got = await Promise.all((session.scope ? [session.scope] : ['']).map((project) => api.listAudit({ project, limit: 500 }).then((r) => r.entries).catch(() => [] as AuditEntry[])));
		return got.flat().sort((a, b) => (b.time || '').localeCompare(a.time || ''));
	});
	const digest = loader((projects) => Promise.all(projects.map((project) => api.digest({ project }).catch(() => null))));

	const shown = $derived(
		(ev.data || []).filter((e) => {
			const ty = short(e.type);
			if (ty === 'agent.usage' && group !== 'agent') return false; // tokens, very frequent
			if (group && !ty.startsWith(group + '.')) return false;
			if (q && !(ty + ' ' + e.subject + ' ' + e.actor + ' ' + e.machine + ' ' + JSON.stringify(e.data || {})).toLowerCase().includes(q.toLowerCase())) return false;
			return true;
		})
	);
	const count = (g: string) => (ev.data || []).filter((e) => short(e.type).startsWith(g + '.')).length;
	const auditShown = $derived((audit.data || []).filter((a) => !q || (a.action + ' ' + a.actor + ' ' + a.target + ' ' + a.detail + ' ' + a.project).toLowerCase().includes(q.toLowerCase())));

	// The one line an event says, from the fields events commonly carry.
	function line(e: Event): string {
		const d = (e.data || {}) as Record<string, any>;
		const pick = (...ks: string[]) => ks.map((k) => d[k]).find((v) => typeof v === 'string' && v);
		const nested = d.check?.check ? (d.check.passed ? '✓ ' : '✗ ') + d.check.check + (d.check.summary ? ' · ' + d.check.summary : '') : '';
		const rev = d.review?.decision ? d.review.decision + (d.review.summary ? ' · ' + d.review.summary : '') : '';
		const del = d.delivery?.status ? d.delivery.status + (d.delivery.summary ? ' · ' + d.delivery.summary : '') : '';
		return nested || rev || del || pick('summary', 'text', 'title', 'reason', 'why', 'detail', 'question', 'answer', 'state', 'step', 'note') || '';
	}
	// The color of an event type; an event whose check failed is red.
	const TONES: Record<string, Tone> = {
		'item.blocked': 'fail', 'policy.violation.blocked': 'fail', 'secret.blocked': 'fail', 'account.limit.reached': 'fail', 'rework.started': 'fail',
		'item.integrated': 'done', 'owner.question.answered': 'done', 'goal.finished': 'done',
		'item.waiting_owner': 'needs', 'owner.question.asked': 'needs', 'system.paused': 'needs',
		'item.step.started': 'work', 'item.claimed': 'work', 'agent.session': 'work', 'autopilot.step': 'review', 'owner.discussion.step': 'review'
	};
	const tone = (e: Event): Tone => {
		const d = (e.data || {}) as any;
		if (d.check && d.check.passed === false) return 'fail';
		return TONES[short(e.type)] || (d.check?.passed ? 'done' : 'idle');
	};
	const isItem = (s: string) => /^I-\d+$/.test(s);
	const copy = (s: string) => navigator.clipboard?.writeText(s).then(() => toast.success(t('copied')));
</script>

<PageHeader title={t('navActivity')} icon={RadioIcon} meta={[session.scope || t('allProjects'), t('evCount', (ev.data || []).length)]} subtitle={t('activityHelp')}>
	{#snippet actions()}
		<div class="relative"><SearchIcon class="absolute left-2 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" /><Input bind:value={q} placeholder={t('evSearch')} class="h-8 w-56 pl-7 text-xs" /></div>
	{/snippet}
</PageHeader>

<Tabs.Root bind:value={tab}>
	<Tabs.List>
		<Tabs.Trigger value="events"><ScrollTextIcon class="size-3.5" />{t('evEvents')}</Tabs.Trigger>
		<Tabs.Trigger value="audit"><ShieldIcon class="size-3.5" />{t('evAudit')}</Tabs.Trigger>
		<Tabs.Trigger value="digest"><NewspaperIcon class="size-3.5" />{t('evDigest')}</Tabs.Trigger>
	</Tabs.List>

	<Tabs.Content value="events" class="mt-3">
		<Loading state={ev}>
			<Section title={t('evEvents')} count={shown.length} pad={false}>
				{#snippet right()}<span class="flex items-center gap-1.5 text-[11px] text-muted-foreground"><Dot tone={session.live === 'on' ? 'done' : 'needs'} pulse={session.live === 'on'} />{session.live === 'on' ? t('live') : t('connecting')}</span>{/snippet}
				<div class="flex gap-1 overflow-x-auto border-b px-3 py-2">
					<button class="num shrink-0 rounded-md px-2 py-1 text-[11px] {group === '' ? 'bg-foreground text-background' : 'text-muted-foreground hover:bg-muted'}" onclick={() => (group = '')}>{t('all')}</button>
					{#each GROUPS.filter((g) => count(g)) as g (g)}<button class="num shrink-0 rounded-md px-2 py-1 text-[11px] {group === g ? 'bg-foreground text-background' : 'text-muted-foreground hover:bg-muted'}" onclick={() => (group = group === g ? '' : g)}>{t('evg_' + g)} {count(g)}</button>{/each}
				</div>
				{#if !shown.length}<div class="p-4"><Empty /></div>{:else}
					<ol class="divide-y">
						{#each shown.slice(0, 300) as e (e.id || e.project + e.seq)}
							{@const ty = short(e.type)}
							<li class="px-4 py-2 text-sm">
								<div class="flex items-start gap-2.5">
									<span class="mt-1.5"><Dot tone={tone(e)} /></span>
									<div class="min-w-0 flex-1">
										<p class="flex flex-wrap items-center gap-x-2 gap-y-0.5">
											<strong class="font-medium">{tm('events', ty) === ty ? ty : tm('events', ty)}</strong>
											{#if e.subject}{#if isItem(e.subject)}<button class="num text-xs hover:underline" onclick={() => openItem(e.project, e.subject)}>{e.subject}</button>{:else}<span class="num text-xs text-muted-foreground">{e.subject}</span>{/if}{/if}
											{#if !session.scope}<span class="num text-[11px] text-muted-foreground">{e.project}</span>{/if}
											<span class="num ml-auto text-[11px] text-muted-foreground" title={when(e.time)}>{ago(e.time)}</span>
										</p>
										{#if line(e)}<p class="line-clamp-2 text-xs text-muted-foreground">{line(e)}</p>{/if}
										<p class="num flex items-center gap-2 text-[10px] text-muted-foreground">
											{#if e.actor}<span>{e.actor}</span>{/if}{#if e.machine}<span>@ {e.machine}</span>{/if}<span>#{e.seq}</span>
											{#if e.data && Object.keys(e.data).length}<button class="ml-1 inline-flex items-center gap-0.5 hover:text-foreground" onclick={() => (open[e.id] = !open[e.id])}>{t('evData')}<ChevronDownIcon class="size-3 transition-transform {open[e.id] ? 'rotate-180' : ''}" /></button>{/if}
										</p>
										{#if open[e.id]}
											<div class="relative mt-1.5">
												<button class="absolute right-1.5 top-1.5 rounded p-1 hover:bg-muted" onclick={() => copy(JSON.stringify(e.data, null, 2))} aria-label={t('copy')}><CopyIcon class="size-3" /></button>
												<pre class="num max-h-72 overflow-auto rounded-md bg-muted/60 p-2 text-[10px] leading-relaxed">{JSON.stringify(e.data, null, 2)}</pre>
											</div>
										{/if}
									</div>
								</div>
							</li>
						{/each}
					</ol>
					{#if shown.length > 300}<p class="border-t px-4 py-2 text-[11px] text-muted-foreground">{t('evMore', shown.length - 300)}</p>{/if}
				{/if}
			</Section>
		</Loading>
	</Tabs.Content>

	<Tabs.Content value="audit" class="mt-3">
		<Loading state={audit}>
			<Section title={t('evAudit')} count={auditShown.length} sub={t('auditHelp')} pad={false}>
				{#if !auditShown.length}<div class="p-4"><Empty /></div>{:else}
					<div class="overflow-x-auto">
						<table class="num w-full text-xs">
							<thead class="eyebrow border-b text-left text-muted-foreground"><tr><th class="px-3 py-2 font-medium">{t('auditWhen')}</th><th class="px-3 py-2 font-medium">{t('auditWho')}</th><th class="px-3 py-2 font-medium">{t('auditAction')}</th><th class="px-3 py-2 font-medium">{t('auditTarget')}</th><th class="px-3 py-2 font-medium">{t('project')}</th><th class="px-3 py-2 font-medium">{t('auditDetail')}</th></tr></thead>
							<tbody class="divide-y">
								{#each auditShown as a, i (i)}
									<tr class="hover:bg-muted/40">
										<td class="whitespace-nowrap px-3 py-1.5 text-muted-foreground" title={when(a.time)}>{ago(a.time)}</td>
										<td class="px-3 py-1.5">{a.actor}{#if a.tokenId}<span class="block text-[10px] text-muted-foreground">{a.tokenId}</span>{/if}</td>
										<td class="px-3 py-1.5 font-medium">{a.action}</td>
										<td class="px-3 py-1.5">{#if isItem(a.target) && a.project}<button class="hover:underline" onclick={() => openItem(a.project, a.target)}>{a.target}</button>{:else}<span class="break-all">{a.target}</span>{/if}</td>
										<td class="px-3 py-1.5 text-muted-foreground">{a.project || '—'}</td>
										<td class="max-w-md px-3 py-1.5 text-muted-foreground"><p class="truncate" title={a.detail}>{a.detail}</p></td>
									</tr>
								{/each}
							</tbody>
						</table>
					</div>
				{/if}
			</Section>
		</Loading>
	</Tabs.Content>

	<Tabs.Content value="digest" class="mt-3">
		<Loading state={digest}>
			<div class="grid gap-4 xl:grid-cols-2">
				{#each (digest.data || []).filter(Boolean) as d (d!.project)}
					<Section title={d!.project} sub={t('digestFrom', ago(d!.generatedAt))}>
						{#snippet right()}<Button size="sm" variant="ghost" onclick={() => copy(d!.text)}><CopyIcon />{t('copy')}</Button>{/snippet}
						<Markdown text={d!.text} />
					</Section>
				{:else}<Empty />{/each}
			</div>
		</Loading>
	</Tabs.Content>
</Tabs.Root>
