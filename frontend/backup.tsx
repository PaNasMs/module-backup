import { useEffect, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import * as Dialog from '@radix-ui/react-dialog'
import { Link, useSearchParams } from 'react-router-dom'
import { mdiBackupRestore, mdiCalendarClockOutline, mdiDatabaseOutline, mdiHistory, mdiFolderOutline, mdiShieldCheckOutline } from '@mdi/js'
import { Button, DialogContent, FolderPicker, Icon, Notice, SectionNav } from '@panasms/ui'
import { registerModule } from '@panasms/runtime'
import { request } from '@panasms/client'
import { ProviderConnect } from '@panasms/external'
import { useRouteTab } from '@panasms/navigation'
import { registerTranslations, translator } from '@panasms/i18n'
import en from './locales/en.json'
import ru from './locales/ru.json'
import uk from './locales/uk.json'
import './backup.css'

registerTranslations('backup', { en, ru, uk })
const tr = translator('backup')
type Repository = { id: string; name: string; path: string; provider: string }
type Source = { kind: string; path: string }
type Plan = { id?: string; name: string; repository: string; sources: Source[]; everyHours: number; keepLast: number; keepDaily: number; keepWeekly: number; enabled: boolean; allowDowntime: boolean; nextRun?: string }
type Job = { file?: string; id: string; kind: string; repository: string; plan?: string; status: string; phase: string; started: string; error?: string; destination?: string; snapshot?: string }
type State = { workspace: { path: string }; repositories: Repository[]; plans: Plan[]; jobs: Job[]; recovery: string[] }
type Snapshot = { id: string; time: string; paths: string[]; tags: string[] }
type Node = { path: string; name: string; type: string; size?: number }
type Container = { Id: string; Name: string }
const api = <T,>(path: string, method = 'GET', body?: unknown) => request<T>(`module-api/backup/${path}`, method, body)
const toast = (message: string) => window.dispatchEvent(new CustomEvent('panasms:toast', { detail: message }))
const key = ['backup-state']
const state = () => api<State>('state')
const initialPlan = (): Plan => ({ name: '', repository: '', sources: [], everyHours: 24, keepLast: 3, keepDaily: 7, keepWeekly: 4, enabled: true, allowDowntime: false })

function Tasks() {
 const query = useQuery({ queryKey: key, queryFn: state, refetchInterval: 5000, retry: false })
 return <div className="backup-tasks">{query.data?.jobs.slice(-5).reverse().map(j => <Link key={j.id} to="/backup/history"><Icon path={mdiBackupRestore} /><span>{tr(j.kind)} · {tr(j.status)}{j.status === 'running' && ` · ${tr(j.phase)}`}</span></Link>)}</div>
}
function Indicator() {
 const previous = useRef<Record<string, string>>({})
 const query = useQuery({ queryKey: key, queryFn: state, refetchInterval: 5000, retry: false })
 useEffect(() => { for (const j of query.data?.jobs || []) { if (previous.current[j.id] === 'running' && j.status !== 'running') toast(`${tr(j.kind)} · ${tr(j.status)}`); previous.current[j.id] = j.status } }, [query.data])
 const job = query.data?.jobs.find(j => j.status === 'running')
 return job ? <Link to="/backup/history" aria-label={`${tr('title')}: ${tr(job.phase)}`}><Icon path={mdiBackupRestore} /><span>{tr(job.phase)}</span></Link> : null
}
function Page() {
 const [part, setPart] = useRouteTab('/backup', ['plans', 'repositories', 'snapshots', 'history', 'recovery'], 'plans')
 const [params, setParams] = useSearchParams()
 const query = useQuery({ queryKey: key, queryFn: state, refetchInterval: 5000, retry: false })
 const client = useQueryClient()
 const [dialog, setDialog] = useState<'repository' | 'plan' | null>(null)
 const [picker, setPicker] = useState<'repository' | 'source' | 'workspace' | null>(null)
 const [repoForm, setRepoForm] = useState({ id: '', name: '', path: '', password: '', create: true, provider: 'local', grant: '' })
 const [workspacePath, setWorkspacePath] = useState('')
 const [planBaseline, setPlanBaseline] = useState('')
 const [keySaved, setKeySaved] = useState(false)
 const [plan, setPlan] = useState<Plan>(initialPlan)
 const [sourceKind, setSourceKind] = useState('files')
 const [sourcePath, setSourcePath] = useState('')
 const [connection, setConnection] = useState('')
 const [confirm, setConfirm] = useState<{ requestId?: string; kind: string; repository?: string; plan?: string; snapshot?: string; path?: string } | null>(null)
 const confirmAction = (body: NonNullable<typeof confirm>) => setConfirm({ ...body, requestId: crypto.randomUUID().replaceAll('-', '') })
 const repository = params.get('repository') || query.data?.repositories[0]?.id || ''
 const snapshot = params.get('snapshot') || ''
 const folder = params.get('folder') || '/'
 const busy = query.data?.jobs.some(j => j.status === 'running') || !!query.data?.recovery.length
 const updateParams = (values: Record<string, string>) => { const next = new URLSearchParams(params); for (const [k, v] of Object.entries(values)) { if (v) next.set(k, v); else next.delete(k) }; setParams(next) }
 const refresh = () => { void client.invalidateQueries({ queryKey: key }); void client.invalidateQueries({ queryKey: ['backup-snapshots'] }) }
 const save = useMutation({ mutationFn: () => dialog === 'repository' ? api('repositories', 'POST', repoForm) : api('plans', 'POST', plan), onSuccess: () => { toast(tr('saved')); setDialog(null); setRepoForm({ id: '', name: '', path: '', password: '', create: true, provider: 'local', grant: '' }); refresh() } })
 const run = useMutation({ mutationFn: (body: NonNullable<typeof confirm>) => api('jobs', 'POST', body), onSuccess: () => { toast(tr('accepted')); setConfirm(null); setPart('history'); refresh() } })
 const workspace = useMutation({ mutationFn: (path: string) => api('workspace', 'POST', { path }), onSuccess: () => { toast(tr('saved')); setWorkspacePath(''); setPicker(null); refresh() } })
 const cancel = useMutation({ mutationFn: (id: string) => api(`cancel/${id}`, 'POST'), onSuccess: refresh })
 const snapshots = useQuery({ queryKey: ['backup-snapshots', repository], queryFn: () => api<Snapshot[]>(`snapshots?repository=${encodeURIComponent(repository)}`), enabled: part === 'snapshots' && !!repository && !busy, retry: false })
 const files = useQuery({ queryKey: ['backup-files', repository, snapshot, folder], queryFn: () => api<Node[]>(`files?repository=${encodeURIComponent(repository)}&snapshot=${snapshot}&path=${encodeURIComponent(folder)}`), enabled: part === 'snapshots' && !!snapshot && !busy, retry: false })
 const containers = useQuery({ queryKey: ['backup-containers'], queryFn: () => api<Container[]>('containers'), enabled: dialog === 'plan' && ['container', 'postgres', 'mysql'].includes(sourceKind), retry: false })
 const connections = useQuery({ queryKey: ['external-connections'], queryFn: () => request<{ id: string; provider: string; email: string }[]>('external/connections'), enabled: dialog === 'repository' && repoForm.provider !== 'local' })
 const linked = connections.data?.filter(c => c.provider === (repoForm.provider === 'drive' ? 'google' : repoForm.provider)) || []
 const selectedConnection = connection || linked[0]?.id || ''
 const addSource = () => { if (!sourcePath && sourceKind !== 'platform') return; setPlan({ ...plan, sources: [...plan.sources, { kind: sourceKind, path: sourceKind === 'platform' ? 'platform' : sourcePath }] }); setSourcePath('') }
 const openPlan = (value?: Plan) => { save.reset(); const draft = value || { ...initialPlan(), repository }; setPlan(draft); setPlanBaseline(JSON.stringify(draft)); setDialog('plan'); setPicker(null) }
 return <div className="backup-page">
  <div className="page-heading"><h1>{tr('title')}</h1><div className="actions">
   {part === 'plans' && <Button className="primary" disabled={!query.data?.repositories.length || busy} onClick={() => openPlan()}>{tr('addPlan')}</Button>}
   {part === 'repositories' && <Button className="primary" disabled={busy} onClick={() => { save.reset(); setKeySaved(false); setRepoForm({ id: '', name: '', path: '', password: '', create: true, provider: 'local', grant: '' }); setDialog('repository'); setPicker(null) }}>{tr('addRepository')}</Button>}
   <Button onClick={() => { refresh(); if (part === 'snapshots') void snapshots.refetch() }}>{tr('refresh')}</Button>
  </div></div>
  {query.isPending && <Notice>{tr('loading')}</Notice>}
  {query.error && <Notice error>{tr('unavailable')} {query.error.message}</Notice>}
  {!!query.data?.recovery.length && <Notice error>{tr('applicationsNeedRecovery')}: {query.data.recovery.join(', ')} <Button disabled={run.isPending} onClick={() => run.mutate({ kind: 'recover' })}>{tr('recover')}</Button></Notice>}
  <div className="section-layout">
   <SectionNav label={tr('title')} tabs={false} value={part} onChange={setPart} items={[
    { id: 'plans', title: tr('plans'), icon: mdiCalendarClockOutline }, { id: 'repositories', title: tr('repositories'), icon: mdiDatabaseOutline },
    { id: 'snapshots', title: tr('snapshots'), icon: mdiFolderOutline }, { id: 'history', title: tr('history'), icon: mdiHistory }, { id: 'recovery', title: tr('recovery'), icon: mdiShieldCheckOutline },
   ]} />
   <section className="section-content backup-content" aria-label={tr(part)}>
    {part === 'plans' && query.data && <>
     {!query.data.plans.length && <div className="backup-panel"><h2>{tr('noPlans')}</h2><p>{tr('noPlansHelp')}</p>{!query.data.repositories.length && <Button onClick={() => setPart('repositories')}>{tr('addRepository')}</Button>}</div>}
     {query.data.plans.map(p => <article className="backup-panel" key={p.id}><h2>{p.name}</h2><p>{query.data.repositories.find(r => r.id === p.repository)?.name}</p><ul>{p.sources.map((s, i) => <li key={i}>{tr(s.kind)}: <span className="backup-path">{s.path}</span></li>)}</ul>
      <p>{p.enabled && p.everyHours ? `${tr('nextRun')}: ${new Date(p.nextRun!).toLocaleString()}` : tr('manualOnly')}</p>
      <div className="actions"><Button className="primary" disabled={busy} onClick={() => { run.reset(); confirmAction({ kind: 'backup', plan: p.id }) }}>{tr('run')}</Button><Button disabled={busy} onClick={() => openPlan(p)}>{tr('edit')}</Button></div></article>)}
    </>}
    {part === 'repositories' && query.data && <>
     {!query.data.repositories.length && <div className="backup-panel"><h2>{tr('noRepositories')}</h2><p>{tr('repositoryHelp')}</p></div>}
     {query.data.repositories.map(r => <article className="backup-panel" key={r.id}><h2>{r.name}</h2><p>{tr(r.provider || 'local')}</p><p className="backup-path">{r.path}</p><div className="actions"><Button onClick={() => { updateParams({ repository: r.id, snapshot: '', folder: '' }); setPart('snapshots') }}>{tr('browse')}</Button><Button disabled={busy} onClick={() => { save.reset(); setKeySaved(false); setRepoForm({ id: r.id, name: r.name, path: r.path, provider: r.provider, password: '', create: false, grant: '' }); setDialog('repository') }}>{tr('reconnect')}</Button><Button disabled={busy} onClick={() => { run.reset(); confirmAction({ kind: 'check', repository: r.id }) }}>{tr('check')}</Button></div></article>)}
    </>}
    {part === 'snapshots' && <div className="backup-panel">
     <label>{tr('repository')}<select value={repository} onChange={e => updateParams({ repository: e.target.value, snapshot: '', folder: '' })}><option value="">{tr('choose')}</option>{query.data?.repositories.map(r => <option key={r.id} value={r.id}>{r.name}</option>)}</select></label>
     {busy && <Notice>{tr('waitForOperation')}</Notice>}
     {snapshots.isFetching && <Notice>{tr('loading')}</Notice>}{snapshots.error && <Notice error>{snapshots.error.message}</Notice>}
     {snapshots.data?.length === 0 && <p>{tr('noSnapshots')}</p>}
     <ul className="backup-list">{snapshots.data?.map(s => <li key={s.id}><div><strong>{new Date(s.time).toLocaleString()}</strong><small>{s.id.slice(0, 12)}</small></div><div className="actions"><Button onClick={() => updateParams({ snapshot: s.id, folder: '/' })}>{tr('browse')}</Button><Button disabled={busy} onClick={() => { run.reset(); confirmAction({ kind: 'restore', repository, snapshot: s.id }) }}>{tr('restoreAll')}</Button><Button disabled={busy} onClick={() => { run.reset(); confirmAction({ kind: 'restoreApplications', repository, snapshot: s.id }) }}>{tr('restoreApplications')}</Button></div></li>)}</ul>
     {snapshot && <><h2>{tr('filesInSnapshot')}</h2><p className="backup-path">{folder}</p><Button disabled={folder === '/'} onClick={() => updateParams({ folder: folder.slice(0, folder.lastIndexOf('/')) || '/' })}>{tr('up')}</Button>
      {files.isFetching && <Notice>{tr('loading')}</Notice>}{files.error && <Notice error>{files.error.message}</Notice>}
      <ul className="backup-list">{files.data?.filter(n => n.path !== folder).map(n => <li key={n.path}><span className="backup-path">{n.name}</span><div className="actions">{n.type === 'dir' && <Button onClick={() => updateParams({ folder: n.path })}>{tr('open')}</Button>}<Button disabled={busy} onClick={() => { run.reset(); confirmAction({ kind: 'restore', repository, snapshot, path: n.path }) }}>{tr('restore')}</Button></div></li>)}</ul></>}
    </div>}
    {part === 'history' && query.data && <div className="backup-panel"><h2>{tr('history')}</h2>{!query.data.jobs.length && <p>{tr('noHistory')}</p>}<ul className="backup-list">{query.data.jobs.slice().reverse().map(j => <li key={j.id}><div><strong>{tr(j.kind)} · {tr(j.status)}</strong><p>{new Date(j.started).toLocaleString()} · {query.data.plans.find(p => p.id === j.plan)?.name || query.data.repositories.find(r => r.id === j.repository)?.name}</p>{j.status === 'running' && <p role="status">{tr(j.phase)}{['preparing', 'stoppingApplications'].includes(j.phase) && ` · ${tr('captureCancellation')}`}</p>}{j.error && <Notice error>{j.error}</Notice>}{j.destination && <p className="backup-path">{tr('destination')}: {j.destination}</p>}{j.file && <a href={`/api/v1/module-api/backup/download?job=${j.id}&path=${encodeURIComponent(j.file)}`}>{tr('download')}</a>}<small>{j.id}</small></div>{j.status === 'running' && <Button disabled={cancel.isPending || j.phase === 'cancelling' || j.phase === 'preparing' || j.phase === 'stoppingApplications'} onClick={() => cancel.mutate(j.id)}>{tr('cancelOperation')}</Button>}</li>)}</ul>{cancel.error && <Notice error>{cancel.error.message}</Notice>}</div>}
    {part === 'recovery' && <div className="backup-panel"><h2>{tr('recovery')}</h2><h3>{tr('workingDisk')}</h3><p>{tr('workingDiskHelp')}</p><p className="backup-path">{query.data?.workspace.path || '/var/lib/panasms-backup'}</p><Button disabled={busy} onClick={() => setPicker('workspace')}>{tr('chooseFolder')}</Button>{picker === 'workspace' && <FolderPicker policy="mount" onChoose={path => { setWorkspacePath(path); setPicker(null) }} />}{workspacePath && <><p className="backup-path">{workspacePath}</p><Button className="primary" disabled={workspace.isPending || busy} onClick={() => workspace.mutate(workspacePath)}>{tr('save')}</Button></>}{workspace.isPending && <Notice>{tr('saving')}</Notice>}{workspace.error && <Notice error>{workspace.error.message}</Notice>}<h3>{tr('recovery')}</h3><p>{tr('recoveryHelp')}</p><ol><li>{tr('recoveryStep1')}</li><li>{tr('recoveryStep2')}</li><li>{tr('recoveryStep3')}</li><li>{tr('recoveryStep4')}</li></ol><Notice>{tr('recoveryUnverified')}</Notice></div>}
   </section>
  </div>
  <Dialog.Root open={!!dialog} onOpenChange={open => { if (!open) { setDialog(null); setPicker(null) } }}><Dialog.Portal><Dialog.Overlay className="dialog-overlay" /><DialogContent className="backup-dialog" variant="form" intent="edit" dirty={dialog === 'plan' ? JSON.stringify(plan) !== planBaseline : !!repoForm.name || !!repoForm.password} busy={save.isPending} message={tr('saving')}
   header={<><Dialog.Title>{tr(dialog === 'plan' ? 'plan' : 'repository')}</Dialog.Title><Dialog.Description>{tr(dialog === 'plan' ? 'planHelp' : 'repositoryHelp')}</Dialog.Description></>}
   footer={!picker && <div className="actions"><Dialog.Close asChild><Button>{tr('cancel')}</Button></Dialog.Close><Button className="primary" disabled={save.isPending || (dialog === 'repository' && (!keySaved || !repoForm.password || (repoForm.provider !== 'local' && !repoForm.grant)))} type="submit" form="backup-editor">{tr('save')}</Button></div>}>
   {picker ? <><Button onClick={() => setPicker(null)}>{tr('back')}</Button><FolderPicker policy="mount" newFolder={picker === 'repository'} initialPath={picker === 'repository' ? repoForm.path : sourcePath} onChoose={path => { if (picker === 'repository') setRepoForm({ ...repoForm, path }); else setSourcePath(path); setPicker(null) }} /></> : dialog === 'repository' ? <form id="backup-editor" className="backup-form" onSubmit={event => { event.preventDefault(); save.mutate() }}>
    <label>{tr('name')}<input required value={repoForm.name} onChange={e => setRepoForm({ ...repoForm, name: e.target.value })} /></label>
    <label>{tr('provider')}<select disabled={!!repoForm.id} value={repoForm.provider} onChange={e => { setRepoForm({ ...repoForm, provider: e.target.value, path: '', grant: '' }); setConnection('') }}><option value="local">{tr('local')}</option><option value="drive">Google Drive</option><option value="dropbox">Dropbox</option><option value="github">{tr("github")}</option></select></label>
    {repoForm.provider !== 'local' && <><label>{tr('account')}<select value={selectedConnection} onChange={e => { setConnection(e.target.value); setRepoForm({ ...repoForm, grant: '' }) }}>{linked.map(c => <option key={c.id} value={c.id}>{c.email}</option>)}</select></label>{!linked.length && <Link to="/profile/connections">{tr('connectAccount')}</Link>}{selectedConnection && !repoForm.grant && <ProviderConnect key={selectedConnection + repoForm.provider} providerId={repoForm.provider === 'drive' ? 'google' : repoForm.provider === 'github' ? 'github' : 'dropbox'} grant={{ connectionId: selectedConnection, consumer: 'backup', capability: repoForm.provider === 'drive' ? 'google-drive' : repoForm.provider === 'github' ? 'github-repositories' : 'dropbox-files' }} onComplete={grant => { if (grant) setRepoForm(current => ({ ...current, grant })) }} />}{repoForm.grant && <p>{tr('authorized')}</p>}</>}
    <label>{tr('repositoryFolder')}<input required value={repoForm.path} onChange={e => setRepoForm({ ...repoForm, path: e.target.value })} /></label>{repoForm.provider === 'local' ? <><Button onClick={() => setPicker('repository')}>{tr('chooseFolder')}</Button><p>{tr('networkHelp')} <Link to="/storage/mounts">{tr('storage')}</Link></p></> : <p>{tr(repoForm.provider === 'github' ? 'githubHelp' : 'cloudFolderHelp')}</p>}
    <label>{tr('mode')}<select disabled={!!repoForm.id} value={String(repoForm.create)} onChange={e => setRepoForm({ ...repoForm, create: e.target.value === 'true' })}><option value="true">{tr('createRepository')}</option><option value="false">{tr('importRepository')}</option></select></label>
    <label>{tr('password')}<input required minLength={8} type="password" autoComplete="new-password" value={repoForm.password} onChange={e => setRepoForm({ ...repoForm, password: e.target.value })} /></label><p>{tr('passwordHelp')}</p><label className="backup-check"><input type="checkbox" checked={keySaved} onChange={e => setKeySaved(e.target.checked)} />{tr('keySaved')}</label>
   </form> : <form id="backup-editor" className="backup-form" onSubmit={event => { event.preventDefault(); save.mutate() }}>
    <label>{tr('name')}<input required value={plan.name} onChange={e => setPlan({ ...plan, name: e.target.value })} /></label><label>{tr('repository')}<select required value={plan.repository} onChange={e => setPlan({ ...plan, repository: e.target.value })}><option value="">{tr('choose')}</option>{query.data?.repositories.map(r => <option key={r.id} value={r.id}>{r.name}</option>)}</select></label>
    <h3>{tr('sources')}</h3><ul className="backup-list">{plan.sources.map((s, i) => <li key={i}><span>{tr(s.kind)}: <span className="backup-path">{s.path}</span></span><Button onClick={() => setPlan({ ...plan, sources: plan.sources.filter((_, index) => index !== i) })}>{tr('remove')}</Button></li>)}</ul>
    <label>{tr('sourceType')}<select value={sourceKind} onChange={e => { setSourceKind(e.target.value); setSourcePath('') }}>{['files', 'sqlite', 'container', 'postgres', 'mysql', 'platform'].map(k => <option key={k} value={k}>{tr(k)}</option>)}</select></label>
    {['files', 'sqlite'].includes(sourceKind) && <><label>{tr('sourcePath')}<input value={sourcePath} onChange={e => setSourcePath(e.target.value)} /></label>{sourceKind === 'files' && <Button onClick={() => setPicker('source')}>{tr('chooseFolder')}</Button>}</>}
    {['container', 'postgres', 'mysql'].includes(sourceKind) && <><label>{tr('application')}<select value={sourcePath} onChange={e => setSourcePath(e.target.value)}><option value="">{tr('choose')}</option>{containers.data?.map(c => <option key={c.Id} value={c.Id}>{c.Name.replace(/^\//, '')}</option>)}</select></label>{containers.error && <Notice error>{containers.error.message}</Notice>}</>}
    <p>{tr(`${sourceKind}Help`)}</p><Button onClick={addSource}>{tr('addSource')}</Button>
    {plan.sources.some(s => s.kind === 'container') && <label className="backup-check"><input type="checkbox" checked={plan.allowDowntime} onChange={e => setPlan({ ...plan, allowDowntime: e.target.checked })} />{tr('downtime')}</label>}
    <h3>{tr('schedule')}</h3><label>{tr('everyHours')}<input type="number" min="0" max="8760" value={plan.everyHours} onChange={e => setPlan({ ...plan, everyHours: Number(e.target.value) })} /></label><label className="backup-check"><input type="checkbox" checked={plan.enabled} onChange={e => setPlan({ ...plan, enabled: e.target.checked })} />{tr('scheduleEnabled')}</label>
    <h3>{tr('retention')}</h3><p>{tr('retentionHelp')}</p>{(['keepLast', 'keepDaily', 'keepWeekly'] as const).map(field => <label key={field}>{tr(field)}<input type="number" min={field === 'keepLast' ? 1 : 0} value={plan[field]} onChange={e => setPlan({ ...plan, [field]: Number(e.target.value) })} /></label>)}
   </form>}
   {save.error && <Notice error>{save.error.message}</Notice>}
  </DialogContent></Dialog.Portal></Dialog.Root>
  <Dialog.Root open={!!confirm} onOpenChange={open => { if (!open) setConfirm(null) }}><Dialog.Portal><Dialog.Overlay className="dialog-overlay" /><DialogContent variant="compact" intent="confirm" busy={run.isPending} message={tr('starting')}
   header={<><Dialog.Title>{tr(confirm?.kind || 'backup')}</Dialog.Title><Dialog.Description>{tr(confirm?.kind === 'restoreApplications' ? 'restoreApplicationsHelp' : confirm?.kind === 'restore' ? 'restoreHelp' : confirm?.kind === 'check' ? 'checkHelp' : 'runHelp')}</Dialog.Description></>}
   footer={<div className="actions"><Dialog.Close asChild><Button data-dialog-cancel>{tr('cancel')}</Button></Dialog.Close><Button className="primary" disabled={run.isPending} onClick={() => confirm && run.mutate(confirm)}>{tr(confirm?.kind || 'backup')}</Button></div>}>
   <p className="backup-path">{confirm?.path || query.data?.plans.find(p => p.id === confirm?.plan)?.name || query.data?.repositories.find(r => r.id === confirm?.repository)?.name}</p>{confirm?.snapshot && <p>{confirm.snapshot.slice(0, 12)}</p>}{run.error && <Notice error>{run.error.message}</Notice>}
  </DialogContent></Dialog.Portal></Dialog.Root>
 </div>
}
registerModule({ id: 'backup', title: tr('title'), path: '/backup', routes: ['plans', 'repositories', 'snapshots', 'history', 'recovery'], icon: mdiBackupRestore, component: Page, tasks: Tasks, backgroundIndicator: Indicator })
