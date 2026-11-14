import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { BrowserRouter, Route, Routes } from 'react-router-dom'
import { modules } from '@fixture/registry'
import { SDK } from '@fixture/sdk'
import '@fixture/style'
import '@fixture/design'
import '@fixture/home'

const params = new URLSearchParams(location.search)
const language = params.get('lang') ?? 'en'
const theme = params.get('theme') ?? 'light'
const surface = params.get('surface') ?? 'solid'
params.delete('theme')
params.delete('surface')
document.documentElement.dataset.theme = theme
document.documentElement.dataset.surface = surface
history.replaceState({}, '', `${location.pathname}${params.size ? `?${params}` : ''}`)
await SDK.i18n.initializeLanguage(language)

await new Promise<void>((resolve, reject) => {
  const script = document.createElement('script')
  script.src = '/api/v1/module-assets/backup/index.js'
  script.onload = () => resolve()
  script.onerror = () => reject(Error('Built backup bundle failed to load'))
  document.head.append(script)
})

const Page = modules().find(module => module.id === 'backup')?.component
if (!Page) throw Error('Backup page did not register')
const client = new QueryClient({ defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false } } })
createRoot(document.getElementById('root')!).render(
  <QueryClientProvider client={client}>
    <BrowserRouter>
      <Routes><Route path="/backup/*" element={<Page />} /></Routes>
    </BrowserRouter>
  </QueryClientProvider>,
)
