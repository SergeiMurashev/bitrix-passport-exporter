import './styles.css'

type Mode = 'all' | 'ids' | 'file'
type ExportFormat = 'xlsx' | 'docx'
type ExportStatus = {
  running: boolean
  can_cancel?: boolean
  cancel_requested?: boolean
  phase: string
  deals_processed: number
  deals_total: number
  tasks_total: number
  deals_with_support?: number
  support_measures_total?: number
  has_last_result: boolean
  last_file_name?: string
  last_error?: string
}
type ApiError = {
  code?: string
  detail?: string
}
type ApiResponse<T> = {
  ok?: boolean
  status?: string
  message?: string
  data?: T
  error?: ApiError
  meta?: Record<string, unknown>
}
type AuthMeData = {
  user?: {
    id: number
    login: string
  }
  expires_at?: string
}
type PortalMeData = {
  ready?: boolean
  user?: {
    id: number
    login: string
  }
}
type ExportRunData = {
  file_name?: string
  content_type?: string
  size_bytes?: number
  download_url?: string
  format?: string
  source?: string
  issues_count?: number
  stats?: {
    deals_total?: number
    tasks_total?: number
    deals_with_support?: number
    support_measures_total?: number
  }
}

type BitrixAuthPayload = {
  domain?: string
  access_token?: string
  refresh_token?: string
  member_id?: string
  user_id?: string
  user_login?: string
  expires_in?: number | string
}

type BX24Like = {
  init: (cb: () => void) => void
  getAuth: (cb: (auth: BitrixAuthPayload) => void) => void
}

declare global {
  interface Window {
    BX24?: BX24Like
  }
}

const root = document.getElementById('root') as HTMLDivElement
root.innerHTML = `
  <div class="page">
    <main class="card">
      <div class="card-head">
        <h1>Выгрузка паспорта проекта</h1>
        <div class="top-controls"></div>
      </div>
      <p class="subtitle">Выберите режим и формат документа, затем скачайте готовый файл.</p>

      <form id="export-form">
        <label class="mode"><input type="radio" name="mode" value="all" checked /> Все сделки из Bitrix24</label>
        <label class="mode"><input type="radio" name="mode" value="ids" /> По ID сделок</label>
        <label class="mode"><input type="radio" name="mode" value="file" /> Из файла сделок (.xls/.xlsx/.html)</label>

        <div id="ids-wrap" class="hidden">
          <label for="deal-ids">ID сделок через запятую</label>
          <input id="deal-ids" type="text" placeholder="Например: 74331,74332,74333" />
        </div>

        <div id="file-wrap" class="hidden">
          <label for="file">Файл выгрузки сделок</label>
          <input id="file" type="file" accept=".xls,.xlsx,.html" />
        </div>

        <div class="format-row">
          <label for="export-format">Формат документа</label>
          <select id="export-format">
            <option value="xlsx" selected>Документ в формате Excel (.xlsx)</option>
            <option value="docx">Документ в формате Word (.docx)</option>
          </select>
        </div>

        <div class="actions">
          <button id="submit" type="submit">Скачать документ в формате Excel</button>
          <button id="cancel-export" class="danger hidden" type="button">Отменить выгрузку</button>
        </div>
        <button id="download-last" class="hidden" type="button">Скачать готовый файл</button>
      </form>
      <div id="progress-wrap" class="progress-wrap hidden" aria-live="polite">
        <div class="progress-head">
          <span id="progress-phase">Подготовка…</span>
        </div>
        <div class="progress-track">
          <div id="progress-bar" class="progress-bar"></div>
        </div>
      </div>
      <section id="status-card" class="status-card hidden" aria-live="polite">
        <h3 class="status-title">Статус выгрузки</h3>
        <p id="status" class="status muted"></p>
        <div id="status-lines" class="status-lines hidden"></div>
      </section>
    </main>
  </div>
`

const form = document.getElementById('export-form') as HTMLFormElement
const idsWrap = document.getElementById('ids-wrap') as HTMLDivElement
const fileWrap = document.getElementById('file-wrap') as HTMLDivElement
const statusEl = document.getElementById('status') as HTMLParagraphElement
const dealIdsEl = document.getElementById('deal-ids') as HTMLInputElement
const fileEl = document.getElementById('file') as HTMLInputElement
const exportFormatEl = document.getElementById('export-format') as HTMLSelectElement
const submitBtn = document.getElementById('submit') as HTMLButtonElement
const cancelExportBtn = document.getElementById('cancel-export') as HTMLButtonElement
const downloadLastBtn = document.getElementById('download-last') as HTMLButtonElement
const progressWrap = document.getElementById('progress-wrap') as HTMLDivElement
const progressBar = document.getElementById('progress-bar') as HTMLDivElement
const progressPhase = document.getElementById('progress-phase') as HTMLSpanElement
const statusCard = document.getElementById('status-card') as HTMLElement
const statusLinesEl = document.getElementById('status-lines') as HTMLDivElement

let mode: Mode = 'all'
let exportFormat: ExportFormat = 'xlsx'
let lastRunning = false
let statusPollTimer: number | null = null

function setAuthState(next: boolean) {
  form.classList.toggle('hidden', !next)
  progressWrap.classList.toggle('hidden', !next)
  stopStatusPolling()
  setProgress(false)
}

setAuthState(false)

function setMode(next: Mode) {
  mode = next
  idsWrap.classList.toggle('hidden', mode !== 'ids')
  fileWrap.classList.toggle('hidden', mode !== 'file')
}

function updateSubmitCaption() {
  if (exportFormat === 'docx') {
    submitBtn.textContent = 'Скачать документ в формате Word'
    return
  }
  submitBtn.textContent = 'Скачать документ в формате Excel'
}

function getFilenameFromDisposition(contentDisposition: string | null): string | null {
  if (!contentDisposition) return null
  const utf8Match = contentDisposition.match(/filename\*=UTF-8''([^;]+)/i)
  if (utf8Match?.[1]) {
    try {
      return decodeURIComponent(utf8Match[1].trim())
    } catch (_err) {
      // ignore
    }
  }
  const plainMatch = contentDisposition.match(/filename="([^"]+)"/i)
  if (plainMatch?.[1]) return plainMatch[1].trim()
  return null
}

function setStatus(text: string, kind: 'ok' | 'err' | 'muted' = 'muted') {
  statusEl.textContent = text
  statusEl.className = `status ${kind}`
  statusCard.classList.toggle('hidden', text.trim() === '')
}

function setStatusLines(lines: Array<{ label: string; value: string | number }>) {
  if (!lines.length) {
    statusLinesEl.classList.add('hidden')
    statusLinesEl.innerHTML = ''
    return
  }
  statusLinesEl.classList.remove('hidden')
  statusLinesEl.innerHTML = lines
    .map((line) => `<div class="status-line"><span>${line.label}</span><strong>${line.value}</strong></div>`)
    .join('')
}

function setProgress(visible: boolean, phaseText = 'Подготовка…') {
  progressWrap.classList.toggle('hidden', !visible)
  progressBar.classList.toggle('running', visible)
  progressPhase.textContent = phaseText
}

function unwrapApiData<T>(payload: unknown): T {
  if (payload && typeof payload === 'object') {
    const obj = payload as Record<string, unknown>
    if ('data' in obj) {
      return obj.data as T
    }
  }
  return payload as T
}

async function readErrorMessage(res: Response): Promise<string> {
  const rawText = await res.text()
  const fallback = rawText || `HTTP ${res.status}`
  try {
    const parsed = JSON.parse(rawText) as ApiResponse<unknown>
    if (parsed?.message) return parsed.message
    if (parsed?.error?.detail) return parsed.error.detail
    if (parsed?.status) return parsed.status
    return fallback
  } catch (_err) {
    return fallback
  }
}

function detectPhase(data: ExportStatus): string {
  if (!data.running) return 'Готово'
  const phase = (data.phase || '').toLowerCase()
  if (phase === 'passport') {
    return 'Сбор данных сделок'
  }
  if (phase === 'tasks') {
    return 'Сбор задач и сборка файла'
  }
  return 'Подготовка экспорта'
}

function humanizePhaseCode(phase: string): string {
  const value = (phase || '').toLowerCase()
  if (value === 'passport') return 'Сбор данных сделок'
  if (value === 'tasks') return 'Сбор задач'
  if (value === 'idle') return 'Ожидание'
  if (value === 'canceled') return 'Отменено'
  if (value === 'error') return 'Ошибка'
  if (value === 'completed') return 'Завершено'
  return value || 'Подготовка'
}

async function checkAuth(): Promise<boolean> {
  try {
    const current = await fetchPortalMe()
    if (current) {
      setAuthState(true)
      setStatus('', 'muted')
      return true
    }
    const bootstrapped = await bootstrapPortalSessionFromBX24()
    if (!bootstrapped) {
      setAuthState(false)
      setStatus('Откройте приложение из портала Bitrix24 (раздел Приложения).', 'err')
      return false
    }
    const afterBootstrap = await fetchPortalMe()
    if (!afterBootstrap) {
      setAuthState(false)
      setStatus('Не удалось инициализировать контекст Bitrix24. Перезапустите приложение из портала.', 'err')
      return false
    }
    setAuthState(true)
    setStatus('', 'muted')
    return true
  } catch (_err) {
    setAuthState(false)
    setStatus('Не удалось инициализировать контекст Bitrix24. Повторите попытку.', 'err')
    return false
  }
}

async function fetchPortalMe(): Promise<PortalMeData | null> {
  const res = await fetch('/api/portal/me')
  if (!res.ok) {
    return null
  }
  const payload = await res.json() as ApiResponse<PortalMeData> | PortalMeData
  return unwrapApiData<PortalMeData>(payload)
}

function getBitrixAuthPayload(): Promise<BitrixAuthPayload | null> {
  return new Promise((resolve) => {
    const bx24 = window.BX24
    if (!bx24) {
      resolve(null)
      return
    }
    bx24.init(() => {
      bx24.getAuth((auth) => resolve(auth || null))
    })
  })
}

async function bootstrapPortalSessionFromBX24(): Promise<boolean> {
  const authPayload = await getBitrixAuthPayload()
  if (!authPayload?.access_token || !authPayload?.domain) {
    return false
  }
  const res = await fetch('/api/portal/session', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(authPayload),
  })
  return res.ok
}

// Словарь уведомлений
function humanizeError(message: string): string {
  const text = (message || '').toLowerCase().trim()
  if (!text) return 'Не удалось сформировать файл. Повторите попытку.'

  const rules: Array<{ test: (t: string) => boolean; userText: string }> = [
    {
      test: (t) => t.includes('no export is running'),
      userText: 'Нет активной выгрузки для отмены.',
    },
    {
      test: (t) => t.includes('export canceled by user') || t.includes('context canceled') || t.includes('http 409'),
      userText: 'Выгрузка отменена.',
    },
    {
      test: (t) => t.includes('deal_ids not found') || t.includes('not found in crm.deal.list'),
      userText: 'Сделка не найдена в Bitrix24. Проверьте ID сделки и попробуйте снова.',
    },
    {
      test: (t) => t.includes('deal_ids must contain positive integers'),
      userText: 'Некорректный формат ID сделки. Укажите одно или несколько положительных чисел через запятую.',
    },
    {
      test: (t) => t.includes('invalid webhook') || t.includes('auth token is invalid or expired'),
      userText: 'Ошибка доступа к Bitrix24: токен портала недействителен или истек.',
    },
    {
      test: (t) => t.includes('portal session') || t.includes('portal context'),
      userText: 'Не найден активный контекст Bitrix24. Откройте приложение из портала и повторите.',
    },
    {
      test: (t) => t.includes('full export is already running'),
      userText: 'Уже выполняется полная выгрузка. Дождитесь завершения и повторите.',
    },
    {
      test: (t) => t.includes('context deadline exceeded') || t.includes('timeout'),
      userText: 'Bitrix24 слишком долго отвечает. Повторите попытку чуть позже.',
    },
    {
      test: (t) => t.includes('failed to load deals') || t.includes('failed to load deals page'),
      userText: 'Не удалось загрузить сделки из Bitrix24. Повторите попытку.',
    },
    {
      test: (t) => t.includes('failed to collect tasks') || t.includes('task'),
      userText: 'Не удалось загрузить задачи по сделкам. Повторите попытку.',
    },
    {
      test: (t) => t.includes('failed to parse input') || t.includes('invalid file field') || t.includes('invalid multipart form') || t.includes('invalid form'),
      userText: 'Не удалось прочитать файл. Проверьте формат (xls/xlsx/html) и попробуйте снова.',
    },
    {
      test: (t) => t.includes('failed to build export file') || t.includes('failed to build xlsx'),
      userText: 'Не удалось сформировать итоговый документ. Повторите попытку.',
    },
    {
      test: (t) => t.includes('method not allowed'),
      userText: 'Некорректный тип запроса. Обновите страницу и повторите.',
    },
    {
      test: (t) => t.includes('http 429'),
      userText: 'Слишком много запросов. Подождите немного и повторите.',
    },
    {
      test: (t) => t.includes('unauthorized') || t.includes('http 401'),
      userText: 'Сессия портала истекла или доступ запрещен. Перезапустите приложение из Bitrix24.',
    },
    {
      test: (t) => t.includes('http 500') || t.includes('http 502') || t.includes('http 503') || t.includes('http 504'),
      userText: 'Временная ошибка сервера. Повторите попытку чуть позже.',
    },
  ]

  for (const rule of rules) {
    if (rule.test(text)) return rule.userText
  }
  return 'Не удалось выполнить выгрузку. Повторите попытку.'
}

form.addEventListener('change', (e) => {
  const target = e.target as HTMLInputElement | HTMLSelectElement
  if (target.name === 'mode') {
    setMode(target.value as Mode)
    if (!lastRunning) {
      setStatus('', 'muted')
    }
  }
  if (target.id === 'export-format') {
    exportFormat = (target.value === 'docx' ? 'docx' : 'xlsx')
    if (!lastRunning) {
      updateSubmitCaption()
    }
  }
})

form.addEventListener('submit', async (e) => {
  e.preventDefault()
  submitBtn.disabled = true
  submitBtn.textContent = 'Формируем...'
  cancelExportBtn.classList.remove('hidden')
  cancelExportBtn.disabled = false
  cancelExportBtn.textContent = 'Отменить выгрузку'
  setProgress(true, 'Подготовка экспорта')
  setStatus(`Формируем документ (${exportFormat === 'docx' ? 'Word' : 'Excel'}). Для полной выгрузки потребуется некоторое время.`, 'muted')

  try {
    const body = new FormData()
    if (mode === 'ids') body.set('deal_ids', dealIdsEl.value.trim())
    if (mode === 'file' && fileEl.files?.[0]) body.set('file', fileEl.files[0])
    body.set('format', exportFormat)

    const res = await fetch('/api/export', { method: 'POST', body })
    if (!res.ok) throw new Error(await readErrorMessage(res))
    const payload = await res.json() as ApiResponse<ExportRunData> | ExportRunData
    const data = unwrapApiData<ExportRunData>(payload)
    const stats = data?.stats || {}
    const fileName = data?.file_name || `passport_and_tasks.${exportFormat}`
    const fileSize = typeof data?.size_bytes === 'number' ? `${Math.max(0, data.size_bytes)} байт` : '-'
    setStatus(
      `Готово. Файл: ${fileName}, размер: ${fileSize}. ` +
      `Сделок: ${stats.deals_total ?? '-'}, задач: ${stats.tasks_total ?? '-'}, ` +
      `сделок с мерами: ${stats.deals_with_support ?? '-'}, мер поддержки: ${stats.support_measures_total ?? '-'}.`,
      'ok',
    )
    setStatusLines([
      { label: 'Режим', value: mode === 'all' ? 'Все сделки' : mode === 'ids' ? 'По ID' : 'Из файла' },
      { label: 'Формат', value: (data?.format || exportFormat).toUpperCase() },
      { label: 'Сделок', value: stats.deals_total ?? '-' },
      { label: 'Задач', value: stats.tasks_total ?? '-' },
      { label: 'Сделок с мерами', value: stats.deals_with_support ?? '-' },
      { label: 'Мер поддержки', value: stats.support_measures_total ?? '-' },
    ])
    downloadLastBtn.classList.remove('hidden')
    downloadLastBtn.textContent = `Скачать готовый файл (${fileName})`
    setProgress(false)
  } catch (error) {
    setStatus(humanizeError((error as Error).message || ''), 'err')
    setStatusLines([])
    setProgress(false)
  } finally {
    submitBtn.disabled = false
    updateSubmitCaption()
    cancelExportBtn.classList.add('hidden')
    cancelExportBtn.disabled = false
  }
})

cancelExportBtn.addEventListener('click', () => {
  void (async () => {
    cancelExportBtn.disabled = true
    try {
      const res = await fetch('/api/export/cancel', { method: 'POST' })
      if (!res.ok) throw new Error(await readErrorMessage(res))
      setStatus('Запрос на отмену отправлен. Ожидайте остановки выгрузки.', 'muted')
      await refreshExportStatus()
    } catch (error) {
      setStatus(humanizeError((error as Error).message || ''), 'err')
      cancelExportBtn.disabled = false
    }
  })()
})

downloadLastBtn.addEventListener('click', () => {
  void (async () => {
    try {
      const res = await fetch('/api/export/download-last')
      if (!res.ok) throw new Error(await readErrorMessage(res))
      const blob = await res.blob()
      const fileName = getFilenameFromDisposition(res.headers.get('content-disposition'))
        || 'bitrix_last_export_passport_and_tasks.xlsx'
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = fileName
      document.body.appendChild(a)
      a.click()
      a.remove()
      URL.revokeObjectURL(url)
    } catch (error) {
      setStatus(humanizeError((error as Error).message || ''), 'err')
    }
  })()
})

async function refreshExportStatus() {
  try {
    const res = await fetch('/api/export/status')
    if (res.status === 401) {
      setAuthState(false)
      setStatus('Сессия портала истекла. Откройте приложение из Bitrix24 повторно.', 'err')
      return
    }
    if (!res.ok) return
    const payload = await res.json() as ApiResponse<ExportStatus> | ExportStatus
    const data = unwrapApiData<ExportStatus>(payload)

    if (data.running) {
      submitBtn.disabled = true
      submitBtn.textContent = 'Формируем...'
      downloadLastBtn.classList.add('hidden')
      if (data.can_cancel) {
        cancelExportBtn.classList.remove('hidden')
        cancelExportBtn.disabled = !!data.cancel_requested
        cancelExportBtn.textContent = data.cancel_requested ? 'Отмена запрошена...' : 'Отменить выгрузку'
      } else {
        cancelExportBtn.classList.add('hidden')
      }
      setProgress(true, detectPhase(data))
      const total = data.deals_total > 0 ? data.deals_total : '?'
      const supportDeals = data.deals_with_support ?? 0
      const supportMeasures = data.support_measures_total ?? 0
      setStatus(
        `Выполняется выгрузка: сделки ${data.deals_processed}/${total}, задачи ${data.tasks_total}, ` +
        `сделок с мерами ${supportDeals}, мер поддержки ${supportMeasures}.`,
        'muted',
      )
      setStatusLines([
        { label: 'Этап', value: humanizePhaseCode(data.phase) },
        { label: 'Сделок обработано', value: `${data.deals_processed}/${total}` },
        { label: 'Задач', value: data.tasks_total },
        { label: 'Сделок с мерами', value: supportDeals },
        { label: 'Мер поддержки', value: supportMeasures },
      ])
    } else {
      setProgress(false)
      if (lastRunning) {
        if (data.last_error) {
          setStatus(`Выгрузка завершилась с ошибкой: ${humanizeError(data.last_error)}`, 'err')
          setStatusLines([
            { label: 'Этап', value: humanizePhaseCode(data.phase || 'error') },
            { label: 'Сделок', value: data.deals_total },
            { label: 'Задач', value: data.tasks_total },
          ])
        } else {
          const supportDeals = data.deals_with_support ?? 0
          const supportMeasures = data.support_measures_total ?? 0
          setStatus(
            `Выгрузка завершена. Сделок: ${data.deals_total}, задач: ${data.tasks_total}, ` +
            `сделок с мерами: ${supportDeals}, мер поддержки: ${supportMeasures}. Нажмите кнопку ниже, чтобы скачать готовый файл.`,
            'ok',
          )
          setStatusLines([
            { label: 'Режим', value: mode === 'all' ? 'Все сделки' : mode === 'ids' ? 'По ID' : 'Из файла' },
            { label: 'Формат', value: exportFormat.toUpperCase() },
            { label: 'Сделок', value: data.deals_total },
            { label: 'Задач', value: data.tasks_total },
            { label: 'Сделок с мерами', value: supportDeals },
            { label: 'Мер поддержки', value: supportMeasures },
          ])
        }
      }
      submitBtn.disabled = false
      updateSubmitCaption()
      cancelExportBtn.classList.add('hidden')
      cancelExportBtn.disabled = false
      cancelExportBtn.textContent = 'Отменить выгрузку'
      if (data.has_last_result) {
        downloadLastBtn.classList.remove('hidden')
        downloadLastBtn.textContent = data.last_file_name
          ? `Скачать готовый файл (${data.last_file_name})`
          : 'Скачать готовый файл'
      } else {
        downloadLastBtn.classList.add('hidden')
        downloadLastBtn.textContent = 'Скачать готовый файл'
      }
    }
    lastRunning = data.running
  } catch (_err) {
    // no-op
  }
}

const statusPollIntervalMs = 7000

function stopStatusPolling() {
  if (statusPollTimer !== null) {
    window.clearInterval(statusPollTimer)
    statusPollTimer = null
  }
}

function startStatusPolling() {
  if (statusPollTimer !== null) return
  statusPollTimer = window.setInterval(() => {
    if (document.visibilityState === 'visible') {
      void refreshExportStatus()
    }
  }, statusPollIntervalMs)
}

document.addEventListener('visibilitychange', () => {
  if (document.visibilityState === 'visible') {
    if (!topUser.classList.contains('hidden')) {
      startStatusPolling()
      void refreshExportStatus()
    }
    return
  }
  stopStatusPolling()
})

updateSubmitCaption()

void (async () => {
  const ok = await checkAuth()
  if (!ok) return
  startStatusPolling()
  await refreshExportStatus()
})()
