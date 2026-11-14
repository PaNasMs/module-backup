import assert from 'node:assert/strict'
import { realpathSync } from 'node:fs'
import { readFile } from 'node:fs/promises'
import { createRequire } from 'node:module'
import { dirname, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const frontend = resolve(process.env.FRONTEND_PACKAGE ?? resolve(root, '../../frontend/package.json'))
const frontendRoot = dirname(frontend)
const require = createRequire(frontend)
const { chromium, expect } = require('@playwright/test')
const bundle = await readFile(new URL('../dist/ui/index.js', import.meta.url), 'utf8')
const stylesheet = await readFile(new URL('../dist/ui/index.css', import.meta.url), 'utf8')
const { createServer } = await import(pathToFileURL(require.resolve('vite')).href)
const { default: react } = await import(pathToFileURL(require.resolve('@vitejs/plugin-react')).href)
const { default: tailwind } = await import(pathToFileURL(require.resolve('@tailwindcss/vite')).href)
const frontendModules = realpathSync(resolve(frontendRoot, 'node_modules'))
const aliases = Object.entries({
  '@fixture/sdk': 'src/app/module-sdk.ts',
  '@fixture/registry': 'src/app/module-registry.ts',
  '@fixture/style': 'src/style.css',
  '@fixture/design': 'src/design-system.css',
  '@fixture/home': 'src/home/components.css',
}).map(([find, path]) => ({ find, replacement: resolve(frontendRoot, path) }))
for (const name of ['react/jsx-runtime', 'react-dom/client', 'react', '@tanstack/react-query', 'react-router-dom'])
  aliases.push({ find: new RegExp('^' + name + '$'), replacement: require.resolve(name) })
const server = await createServer({
  configFile: false,
  root: resolve(root, 'tests'),
  plugins: [react(), tailwind()],
  resolve: { alias: aliases },
  server: { host: '127.0.0.1', port: 15173, strictPort: true, fs: { allow: [root, frontendRoot, frontendModules] } },
})
await server.listen()
const labels = {
  en: { browse: 'Browse', repository: 'Repository' },
  ru: { browse: 'Просмотреть', repository: 'Хранилище' },
  uk: { browse: 'Переглянути', repository: 'Сховище' },
}

async function discardEditedRepository(page, edit, verify) {
  await page.getByRole('button', { name: 'Connect repository', exact: true }).click()
  const dialog = page.getByRole('dialog').first()
  await edit(dialog)
  await dialog.locator('.modal-footer').getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Discard unsaved changes?', exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Keep editing', exact: true }).click()
  await verify(dialog)
  await dialog.locator('.modal-footer').getByRole('button', { name: 'Cancel', exact: true }).click()
  await page.getByRole('button', { name: 'Discard changes', exact: true }).click()
  await expect(page.getByRole('dialog')).toHaveCount(0)
}

async function revertRepositoryEdit(page, edit, revert) {
  await page.getByRole('button', { name: 'Connect repository', exact: true }).click()
  const dialog = page.getByRole('dialog').first()
  await edit(dialog)
  await revert(dialog)
  await dialog.locator('.modal-footer').getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expect(page.getByRole('heading', { name: 'Discard unsaved changes?', exact: true })).toHaveCount(0)
}

let browser
const errors = []
try {
  browser = await chromium.launch({
    executablePath: process.env.CHROME_PATH || undefined,
    headless: true,
    args: ['--no-sandbox'],
  })
  for (const [language, text] of Object.entries(labels)) {
    for (const width of [320, 1440]) {
      const page = await browser.newPage({ viewport: { width, height: 900 } })
      page.on('pageerror', error => errors.push(error.message))
      page.on('console', message => { if (message.type() === 'error') errors.push(message.text()) })
      const requests = []
      const writes = []
      const unexpected = []
      await page.route('**/api/v1/**', route => {
        const url = new URL(route.request().url())
        if (url.pathname.endsWith('/module-assets/backup/index.js'))
          return route.fulfill({ contentType: 'text/javascript', body: bundle })
        if (url.pathname.endsWith('/module-assets/backup/index.css'))
          return route.fulfill({ contentType: 'text/css', body: stylesheet })
        if (/\/module-api\/backup\/(repositories|plans)$/.test(url.pathname) && route.request().method() === 'POST') {
          writes.push(route.request().postDataJSON())
          return route.fulfill({ json: { id: 'synthetic-repository' } })
        }
        if (url.pathname.endsWith('/module-api/backup/snapshots')) {
          requests.push(url.searchParams.get('repository'))
          return route.fulfill({ json: [] })
        }
        if (url.pathname.endsWith('/external/providers'))
          return route.fulfill({ json: { google: { enabled: true, login: true }, dropbox: { enabled: true, login: true }, github: { enabled: true, login: true } } })
        if (url.pathname.endsWith('/external/connections'))
          return route.fulfill({ json: [
            { id: 'google-one', provider: 'google', email: 'one@example.test' },
            { id: 'google-two', provider: 'google', email: 'two@example.test' },
          ] })
        if (url.pathname.endsWith('/external/google/start'))
          return route.fulfill({ json: { status: 'granted', grantId: 'synthetic-grant' } })
        if (url.pathname.endsWith('/manage') && url.searchParams.get('view') === 'mount-folders')
          return route.fulfill({ json: { roots: [{ name: 'Synthetic mount', path: '/synthetic-mount' }], folders: [] } })
        const values = {
          session: { username: 'operator', role: 'admin' },
          preferences: { language, theme: 'light', taskbar: [], taskbarMobile: [] },
          avatar: {}, wallpaper: {}, notifications: [], jobs: [],
          modules: { installed: [{ id: 'backup', title: 'Backup', version: '0.1.8', enabled: true, files: { 'ui/index.js': 'fixture' } }] },
          state: {
            workspace: { path: '/workspace' }, plans: [], jobs: [], recovery: [],
            repositories: [
              { id: 'alpha', name: 'alpha', path: '/alpha', provider: 'local' },
              { id: 'beta', name: 'beta', path: '/beta', provider: 'local' },
              { id: 'cloud', name: 'Cloud repository', path: 'Backups', provider: 'drive' },
            ],
          },
        }
        const key = url.searchParams.get('view') ?? url.pathname.split('/').pop()
        if (Object.hasOwn(values, key)) return route.fulfill({ json: values[key] })
        unexpected.push(`${route.request().method()} ${url.pathname}${url.search}`)
        return route.fulfill({ json: {} })
      })

      const original = `http://127.0.0.1:15173/backup/repositories?repository=alpha&snapshot=old&folder=%2Fold&keep=value&lang=${language}`
      await page.goto(original)
      await page.getByRole('article').filter({ has: page.getByRole('heading', { name: 'beta', exact: true }) })
        .getByRole('button', { name: text.browse, exact: true }).click()
      const repository = page.getByRole('combobox', { name: text.repository, exact: true })
      await expect(repository).toHaveValue('beta')
      let url = new URL(page.url())
      assert.equal(url.pathname, '/backup/snapshots')
      assert.equal(url.searchParams.get('repository'), 'beta')
      assert.equal(url.searchParams.get('snapshot'), null)
      assert.equal(url.searchParams.get('folder'), null)
      assert.equal(url.searchParams.get('keep'), 'value')
      await expect.poll(() => requests.at(-1)).toBe('beta')
      assert.ok(requests.every(id => id === 'beta'))
      await page.goBack()
      await expect(page).toHaveURL(original)
      await page.goForward()
      await expect(repository).toHaveValue('beta')
      await page.reload()
      await expect(repository).toHaveValue('beta')
      await repository.selectOption('alpha')
      await expect(repository).toHaveValue('alpha')
      await expect.poll(() => requests.at(-1)).toBe('alpha')
      url = new URL(page.url())
      assert.equal(url.pathname, '/backup/snapshots')
      assert.equal(url.searchParams.get('repository'), 'alpha')
      assert.equal(url.searchParams.get('keep'), 'value')

      if (language === 'en' && width === 1440) {
        await page.locator('nav.section-nav').getByRole('button', { name: 'Repositories', exact: true }).click()
        const alpha = page.getByRole('article').filter({ has: page.getByRole('heading', { name: 'alpha', exact: true }) })
        await alpha.getByRole('button', { name: 'Reconnect', exact: true }).click()
        let reconnect = page.getByRole('dialog').first()
        await expect(reconnect.getByLabel('Name')).toHaveValue('alpha')
        await expect(reconnect.getByLabel('Repository folder')).toHaveValue('/alpha')
        await reconnect.locator('.modal-footer').getByRole('button', { name: 'Cancel', exact: true }).click()
        await expect(page.getByRole('dialog')).toHaveCount(0)
        await expect(page.getByRole('heading', { name: 'Discard unsaved changes?', exact: true })).toHaveCount(0)

        await revertRepositoryEdit(page, dialog => dialog.getByLabel('Repository folder').fill('/temporary'), dialog => dialog.getByLabel('Repository folder').fill(''))
        await revertRepositoryEdit(page, dialog => dialog.getByLabel('Destination type').selectOption('drive'), dialog => dialog.getByLabel('Destination type').selectOption('local'))
        await revertRepositoryEdit(page, dialog => dialog.getByLabel('Repository mode').selectOption('false'), dialog => dialog.getByLabel('Repository mode').selectOption('true'))
        await revertRepositoryEdit(page, dialog => dialog.getByLabel('Encryption password').fill('synthetic-password'), dialog => dialog.getByLabel('Encryption password').fill(''))
        await revertRepositoryEdit(page, dialog => dialog.getByLabel('I have stored this password outside the NAS.').check(), dialog => dialog.getByLabel('I have stored this password outside the NAS.').uncheck())

        await discardEditedRepository(page, dialog => dialog.getByLabel('Name').fill('temporary name'), dialog => expect(dialog.getByLabel('Name')).toHaveValue('temporary name'))
        await discardEditedRepository(page, dialog => dialog.getByLabel('Repository folder').fill('/synthetic/path'), dialog => expect(dialog.getByLabel('Repository folder')).toHaveValue('/synthetic/path'))
        await discardEditedRepository(page, dialog => dialog.getByLabel('Destination type').selectOption('drive'), dialog => expect(dialog.getByLabel('Destination type')).toHaveValue('drive'))
        await discardEditedRepository(page, dialog => dialog.getByLabel('Repository mode').selectOption('false'), dialog => expect(dialog.getByLabel('Repository mode')).toHaveValue('false'))
        await discardEditedRepository(page, dialog => dialog.getByLabel('Encryption password').fill('synthetic-password'), dialog => expect(dialog.getByLabel('Encryption password')).toHaveValue('synthetic-password'))
        await discardEditedRepository(page, dialog => dialog.getByLabel('I have stored this password outside the NAS.').check(), dialog => expect(dialog.getByLabel('I have stored this password outside the NAS.')).toBeChecked())

        const cloud = page.getByRole('article').filter({ has: page.getByRole('heading', { name: 'Cloud repository', exact: true }) })
        await cloud.getByRole('button', { name: 'Reconnect', exact: true }).click()
        reconnect = page.getByRole('dialog').first()
        await expect(reconnect.getByLabel('Account')).toHaveValue('google-one')
        await reconnect.locator('.modal-footer').getByRole('button', { name: 'Cancel', exact: true }).click()
        await expect(page.getByRole('dialog')).toHaveCount(0)
        await expect(page.getByRole('heading', { name: 'Discard unsaved changes?', exact: true })).toHaveCount(0)

        await cloud.getByRole('button', { name: 'Reconnect', exact: true }).click()
        reconnect = page.getByRole('dialog').first()
        const accounts = reconnect.getByLabel('Account')
        await expect(accounts).toHaveValue('google-one')
        await accounts.selectOption('google-two')
        await accounts.selectOption('google-one')
        await reconnect.locator('.modal-footer').getByRole('button', { name: 'Cancel', exact: true }).click()
        await expect(page.getByRole('dialog')).toHaveCount(0)
        await expect(page.getByRole('heading', { name: 'Discard unsaved changes?', exact: true })).toHaveCount(0)

        await cloud.getByRole('button', { name: 'Reconnect', exact: true }).click()
        reconnect = page.getByRole('dialog').first()
        const changedAccounts = reconnect.getByLabel('Account')
        await expect(changedAccounts).toHaveValue('google-one')
        await changedAccounts.selectOption('google-two')
        await reconnect.locator('.modal-footer').getByRole('button', { name: 'Cancel', exact: true }).click()
        await expect(page.getByRole('heading', { name: 'Discard unsaved changes?', exact: true })).toBeVisible()
        await page.getByRole('button', { name: 'Keep editing', exact: true }).click()
        await expect(changedAccounts).toHaveValue('google-two')
        await reconnect.locator('.modal-footer').getByRole('button', { name: 'Cancel', exact: true }).click()
        await page.getByRole('button', { name: 'Discard changes', exact: true }).click()
        await expect(page.getByRole('dialog')).toHaveCount(0)

        await cloud.getByRole('button', { name: 'Reconnect', exact: true }).click()
        reconnect = page.getByRole('dialog').first()
        await reconnect.getByRole('button', { name: 'Allow Google Drive access', exact: true }).click()
        const grantDialog = page.getByRole('dialog').last()
        await grantDialog.getByRole('button', { name: 'Continue', exact: true }).click()
        await expect(page.getByText('Account access granted', { exact: true })).toBeVisible()
        await expect(page.getByRole('dialog')).toHaveCount(1)
        await reconnect.locator('.modal-footer').getByRole('button', { name: 'Cancel', exact: true }).click()
        await expect(page.getByRole('heading', { name: 'Discard unsaved changes?', exact: true })).toBeVisible()
        await page.getByRole('button', { name: 'Keep editing', exact: true }).click()
        await expect(page.getByText('Account access granted', { exact: true })).toBeVisible()
        await reconnect.locator('.modal-footer').getByRole('button', { name: 'Cancel', exact: true }).click()
        await page.getByRole('button', { name: 'Discard changes', exact: true }).click()
        await expect(page.getByRole('dialog')).toHaveCount(0)

        await page.getByRole('button', { name: 'Connect repository', exact: true }).click()
        const grantRevert = page.getByRole('dialog').first()
        const provider = grantRevert.getByLabel('Destination type')
        await provider.selectOption('drive')
        await expect(grantRevert.getByLabel('Account')).toHaveValue('google-one')
        await grantRevert.getByRole('button', { name: 'Allow Google Drive access', exact: true }).click()
        await page.getByRole('dialog').last().getByRole('button', { name: 'Continue', exact: true }).click()
        await expect(page.getByText('Account access granted', { exact: true })).toBeVisible()
        await provider.selectOption('local')
        await grantRevert.locator('.modal-footer').getByRole('button', { name: 'Cancel', exact: true }).click()
        await expect(page.getByRole('dialog')).toHaveCount(0)
        await expect(page.getByRole('heading', { name: 'Discard unsaved changes?', exact: true })).toHaveCount(0)

        await page.getByRole('button', { name: 'Connect repository', exact: true }).click()
        const newRepository = page.getByRole('dialog').first()
        await newRepository.getByLabel('Name').fill('temporary name')
        await newRepository.getByLabel('Name').fill('')
        await newRepository.locator('.modal-footer').getByRole('button', { name: 'Cancel', exact: true }).click()
        await expect(page.getByRole('dialog')).toHaveCount(0)
        await expect(page.getByRole('heading', { name: 'Discard unsaved changes?', exact: true })).toHaveCount(0)

        await page.locator('nav.section-nav').getByRole('button', { name: 'Backup plans', exact: true }).click()
        await page.getByRole('button', { name: 'Create plan', exact: true }).click()
        let planDialog = page.getByRole('dialog').first()
        await planDialog.getByRole('button', { name: 'Choose folder', exact: true }).click()
        await expect(planDialog.locator('.folder-picker')).toBeVisible()
        await planDialog.getByRole('button', { name: 'Back', exact: true }).click()
        await planDialog.locator('.modal-footer').getByRole('button', { name: 'Cancel', exact: true }).click()
        await expect(page.getByRole('dialog')).toHaveCount(0)
        await expect(page.getByRole('heading', { name: 'Discard unsaved changes?', exact: true })).toHaveCount(0)

        await page.getByRole('button', { name: 'Create plan', exact: true }).click()
        planDialog = page.getByRole('dialog').first()
        await planDialog.getByLabel('Source path').fill('/synthetic/pending-source')
        await planDialog.locator('.modal-footer').getByRole('button', { name: 'Cancel', exact: true }).click()
        await expect(page.getByRole('heading', { name: 'Discard unsaved changes?', exact: true })).toBeVisible()
        await page.getByRole('button', { name: 'Keep editing', exact: true }).click()
        await expect(planDialog.getByLabel('Source path')).toHaveValue('/synthetic/pending-source')
        await planDialog.locator('.modal-footer').getByRole('button', { name: 'Cancel', exact: true }).click()
        await page.getByRole('button', { name: 'Discard changes', exact: true }).click()
        await expect(page.getByRole('dialog')).toHaveCount(0)
        await page.getByRole('button', { name: 'Create plan', exact: true }).click()
        planDialog = page.getByRole('dialog').first()
        await expect(planDialog.getByLabel('Source path')).toHaveValue('')
        await expect(planDialog.getByLabel('Source type')).toHaveValue('files')
        await planDialog.locator('.modal-footer').getByRole('button', { name: 'Cancel', exact: true }).click()
        await expect(page.getByRole('dialog')).toHaveCount(0)
        assert.deepEqual(writes, [], 'Cancel and Discard must not submit repository or plan changes')
      }

      await page.close()
      assert.deepEqual(unexpected, [])
      const coverage = language === 'en' && width === 1440
        ? 'repository navigation and dialog drafts'
        : 'repository navigation'
      console.log(`${language} ${width}: ${coverage} passed`)
    }
  }
  assert.deepEqual(errors, [])
} finally {
  await browser?.close()
  await server.close()
}
