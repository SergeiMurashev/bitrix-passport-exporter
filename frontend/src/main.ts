import './styles.css'

type Mode = 'all' | 'ids' | 'file'
type ExportStatus = {
  running: boolean
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
      <h1>Выгрузка паспорта проекта + задач</h1>
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

        <button id="submit" type="submit">Сформировать XLSX</button>
        <button id="download-last" class="hidden" type="button">Скачать готовый файл</button>
      </form>

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
const downloadLastBtn = document.getElementById('download-last') as HTMLButtonElement

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

form.addEventListener('change', (e) => {
  const target = e.target as HTMLInputElement
  if (target.name === 'mode') setMode(target.value as Mode)
})

form.addEventListener('submit', async (e) => {
  e.preventDefault()
  submitBtn.disabled = true
  submitBtn.textContent = 'Формируем...'
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
  } catch (error) {
    setStatus((error as Error).message || 'Ошибка экспорта', 'err')
  } finally {
    submitBtn.disabled = false
    submitBtn.textContent = 'Сформировать XLSX'
  }
})

downloadLastBtn.addEventListener('click', () => {
  const a = document.createElement('a')
  a.href = '/api/export/download-last'
  document.body.appendChild(a)
  a.click()
  a.remove()
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
      const total = data.deals_total > 0 ? data.deals_total : '?'
      setStatus(`Выполняется выгрузка: фаза ${data.phase}, сделки ${data.deals_processed}/${total}, задачи ${data.tasks_total}.`, 'muted')
    } else {
      if (lastRunning) {
        if (data.last_error) {
          setStatus(`Выгрузка завершилась с ошибкой: ${data.last_error}`, 'err')
        } else {
          setStatus('Выгрузка завершена. Нажмите кнопку ниже, чтобы скачать готовый файл.', 'ok')
        }
      }
      submitBtn.disabled = false
      submitBtn.textContent = 'Сформировать XLSX'
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

setInterval(refreshExportStatus, 3000)
void refreshExportStatus()
