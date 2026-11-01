import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import vm from 'node:vm'

let module
const jobs = [
  { id: 'old', kind: 'backup', status: 'succeeded' },
  { id: 'active', kind: 'backup', status: 'running', phase: 'backup' },
]
const state = { jobs, recovery: [] }
const calls = []
const node = (type, props) => ({ type, props })
const sdk = {
  react: {},
  jsx: { jsx: node, jsxs: node },
  query: { useQuery: () => ({ data: state }) },
  dialog: {},
  router: { Link: 'link' },
  ui: { Icon: 'icon' },
  runtime: { registerModule: value => { module = value } },
  client: { request: async (path, method, body) => {
    calls.push({ path, method, body })
    if (path.endsWith('/state')) return state
    assert.equal(path, 'module-api/backup/jobs/clear')
    assert.equal(method, 'POST')
    assert.deepEqual(JSON.parse(JSON.stringify(body)), {})
    jobs[0].dismissed = true
    return { cleared: 1 }
  } },
  i18n: { registerTranslations() {}, translator: () => key => key },
  navigation: {}, external: {},
}
vm.runInNewContext(await readFile(new URL('../dist/ui/index.js', import.meta.url), 'utf8'), { PaNasMsSDK: sdk })
assert.equal(module.id, 'backup')
assert.equal(await module.taskHistory.status().then(s => s.canClear), true)
assert.equal(module.tasks().props.children.length, 2)
await module.taskHistory.clear()
assert.equal(await module.taskHistory.status().then(s => s.canClear), false)
assert.equal(module.tasks().props.children.length, 1)
assert.equal(module.tasks().props.children[0].props.children[1].props.children[2], 'running')
assert.equal(state.jobs.length, 2)
state.jobs[0].dismissed = false
state.recovery = ['pending-application']
assert.equal(await module.taskHistory.status().then(s => s.canClear), false)
console.log('Built Backup module task-history contract passed')
