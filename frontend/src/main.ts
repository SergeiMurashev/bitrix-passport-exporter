import './styles.css'

type Mode = 'all' | 'ids' | 'file'
type ExportStatus = {
  running: boolean
  can_cancel?: boolean
  cancel_requested?: boolean
  phase: string
  deals_processed: number
  deals_total: number
  tasks_total: number
  has_last_result: boolean
  last_file_name?: string
  last_error?: string
}

const root = document.getElementById('root') as HTMLDivElement
root.innerHTML = `
  <div class="page">
    <main class="card">
      <h1>Выгрузка паспорта проекта</h1>
      <p class="subtitle">Выберите один режим и сформируйте XLSX.</p>

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

        <div class="actions">
          <button id="submit" type="submit">Сформировать XLSX</button>
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

      <p id="status" class="status"></p>
    </main>
  </div>
`

const form = document.getElementById('export-form') as HTMLFormElement
const idsWrap = document.getElementById('ids-wrap') as HTMLDivElement
const fileWrap = document.getElementById('file-wrap') as HTMLDivElement
const statusEl = document.getElementById('status') as HTMLParagraphElement
const dealIdsEl = document.getElementById('deal-ids') as HTMLInputElement
const fileEl = document.getElementById('file') as HTMLInputElement
const submitBtn = document.getElementById('submit') as HTMLButtonElement
const cancelExportBtn = document.getElementById('cancel-export') as HTMLButtonElement
const downloadLastBtn = document.getElementById('download-last') as HTMLButtonElement
const progressWrap = document.getElementById('progress-wrap') as HTMLDivElement
const progressBar = document.getElementById('progress-bar') as HTMLDivElement
const progressPhase = document.getElementById('progress-phase') as HTMLSpanElement

let mode: Mode = 'all'
let lastRunning = false

function setMode(next: Mode) {
  mode = next
  idsWrap.classList.toggle('hidden', mode !== 'ids')
  fileWrap.classList.toggle('hidden', mode !== 'file')
}

function setStatus(text: string, kind: 'ok' | 'err' | 'muted' = 'muted') {
  statusEl.textContent = text
  statusEl.className = `status ${kind}`
}

function setProgress(visible: boolean, phaseText = 'Подготовка…') {
  progressWrap.classList.toggle('hidden', !visible)
  progressBar.classList.toggle('running', visible)
  progressPhase.textContent = phaseText
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
      test: (t) => t.includes('invalid webhook') || t.includes('webhook is invalid or expired'),
      userText: 'Ошибка доступа к Bitrix24: webhook недействителен или истек.',
    },
    {
      test: (t) => t.includes('server is not configured: bitrix_webhook_url is empty'),
      userText: 'Сервер не настроен: не указан webhook Bitrix24.',
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
      test: (t) => t.includes('failed to build xlsx'),
      userText: 'Не удалось сформировать итоговый Excel-файл. Повторите попытку.',
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
      userText: 'Доступ запрещен. Перезагрузите страницу и повторите попытку.',
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
  const target = e.target as HTMLInputElement
  if (target.name === 'mode') {
    setMode(target.value as Mode)
    if (!lastRunning) {
      setStatus('', 'muted')
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
  setStatus('Формируем файл. Для полной выгрузки потребуется некоторое время.', 'muted')

  try {
    const body = new FormData()
    if (mode === 'ids') body.set('deal_ids', dealIdsEl.value.trim())
    if (mode === 'file' && fileEl.files?.[0]) body.set('file', fileEl.files[0])

    const res = await fetch('/api/export', { method: 'POST', body })
    if (!res.ok) throw new Error((await res.text()) || `HTTP ${res.status}`)

    const blob = await res.blob()
    const fileName = res.headers.get('x-export-file-name') || 'passport_and_tasks.xlsx'
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = fileName
    document.body.appendChild(a)
    a.click()
    a.remove()
    URL.revokeObjectURL(url)

    setStatus(`Готово. Сделок: ${res.headers.get('x-export-deals-total') || '-'}, задач: ${res.headers.get('x-export-tasks-total') || '-'}.`, 'ok')
    setProgress(false)
  } catch (error) {
    setStatus(humanizeError((error as Error).message || ''), 'err')
    setProgress(false)
  } finally {
    submitBtn.disabled = false
    submitBtn.textContent = 'Сформировать XLSX'
    cancelExportBtn.classList.add('hidden')
    cancelExportBtn.disabled = false
  }
})

cancelExportBtn.addEventListener('click', () => {
  void (async () => {
    cancelExportBtn.disabled = true
    try {
      const res = await fetch('/api/export/cancel', { method: 'POST' })
      if (!res.ok) throw new Error((await res.text()) || `HTTP ${res.status}`)
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
      if (!res.ok) throw new Error((await res.text()) || `HTTP ${res.status}`)
      const blob = await res.blob()
      const fileName = 'bitrix_last_export_passport_and_tasks.xlsx'
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
    if (!res.ok) return
    const data = await res.json() as ExportStatus

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
      setStatus(`Выполняется выгрузка: фаза ${data.phase}, сделки ${data.deals_processed}/${total}, задачи ${data.tasks_total}.`, 'muted')
    } else {
      setProgress(false)
      if (lastRunning) {
        if (data.last_error) {
          setStatus(`Выгрузка завершилась с ошибкой: ${humanizeError(data.last_error)}`, 'err')
        } else {
          setStatus('Выгрузка завершена. Нажмите кнопку ниже, чтобы скачать готовый файл.', 'ok')
        }
      }
      submitBtn.disabled = false
      submitBtn.textContent = 'Сформировать XLSX'
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
let statusPollTimer: number | null = null

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
    startStatusPolling()
    void refreshExportStatus()
    return
  }
  stopStatusPolling()
})

startStatusPolling()
void refreshExportStatus()
