import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { createRequire } from 'node:module'

// This integration test uses the core frontend's browser dependencies and running dev server.
const require = createRequire(process.env.FRONTEND_PACKAGE ?? new URL('../../../frontend/package.json', import.meta.url))
const { chromium, expect } = require('@playwright/test')
const base = process.env.FRONTEND_TEST_URL ?? 'http://127.0.0.1:15173'
const bundle = await readFile(new URL('../dist/ui/index.js', import.meta.url), 'utf8')
const browser = await chromium.launch({
  executablePath: process.env.CHROME_PATH || '/opt/google/chrome/chrome',
  headless: true,
  args: ['--no-sandbox'],
})
const labels = {
  en: { browse: 'Browse', repository: 'Repository' },
  ru: { browse: 'Просмотреть', repository: 'Хранилище' },
  uk: { browse: 'Переглянути', repository: 'Сховище' },
}

try {
  for (const [language, text] of Object.entries(labels)) {
    for (const width of [320, 1440]) {
      const page = await browser.newPage({ viewport: { width, height: 900 } })
      const requests = []
      await page.route('**/api/v1/**', route => {
        const url = new URL(route.request().url())
        if (url.pathname.endsWith('/module-assets/backup/index.js'))
          return route.fulfill({ contentType: 'text/javascript', body: bundle })
        if (url.pathname.endsWith('/module-api/backup/snapshots')) {
          requests.push(url.searchParams.get('repository'))
          return route.fulfill({ json: [] })
        }
        const values = {
          session: { username: 'operator', role: 'admin' },
          preferences: { language, theme: 'light', taskbar: [], taskbarMobile: [] },
          avatar: {}, wallpaper: {}, notifications: [], jobs: [],
          modules: { installed: [{ id: 'backup', title: 'Backup', version: '0.1.7', enabled: true, files: { 'ui/index.js': 'fixture' } }] },
          state: {
            workspace: { path: '/workspace' }, plans: [], jobs: [], recovery: [],
            repositories: ['alpha', 'beta'].map(id => ({ id, name: id, path: '/' + id, provider: 'local' })),
          },
        }
        const key = url.searchParams.get('view') ?? url.pathname.split('/').pop()
        return Object.hasOwn(values, key)
          ? route.fulfill({ json: values[key] })
          : route.fulfill({ status: 503, json: { error: 'Unavailable in fixture' } })
      })

      const original = `${base}/backup/repositories?repository=alpha&snapshot=old&folder=%2Fold&keep=value`
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
      await page.close()
      console.log(`${language} ${width}: repository navigation passed`)
    }
  }
} finally {
  await browser.close()
}
