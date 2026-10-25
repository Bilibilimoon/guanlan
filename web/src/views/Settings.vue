<script setup>
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { getJSON, postJSON } from '../api'
import { fmtBytes } from '../fmt'

const { t, locale } = useI18n()
const cfg = ref(null)
const meta = ref(null)
const saved = ref('')

const SAMPLE_CHOICES = [1, 2, 5, 10, 15]
const DB_CHOICES = [5, 10, 15, 30, 60]

onMounted(async () => {
  try { cfg.value = await getJSON('api/config') } catch (e) { /* 忽略 */ }
  try { meta.value = await getJSON('api/meta') } catch (e) { /* 忽略 */ }
})

async function save(patch) {
  try {
    cfg.value = await postJSON('api/config', patch)
    saved.value = t('ctrl.saved')
    setTimeout(() => (saved.value = ''), 1500)
  } catch (e) { /* 忽略 */ }
}

function setSample(v) { save({ intervalMs: v * 1000 }) }
function setDb(v) { save({ dbIntervalMs: v * 1000 }) }
function setRetain(k, v) {
  const rd = { ...cfg.value.retainDays, [k]: v }
  save({ retainDays: rd })
}
function setLang(v) {
  locale.value = v
  localStorage.setItem('guanlan.lang', v)
}

// 存储估算
const estimate = computed(() => {
  if (!cfg.value || !meta.value) return null
  const dbMs = cfg.value.dbIntervalMs
  const nMetrics = Math.max((meta.value.metrics || []).length, 30)
  const bytesPerPoint = 34
  const perDayL0 = (86400 / (dbMs / 1000)) * nMetrics * bytesPerPoint
  const rd = cfg.value.retainDays
  return {
    l0: perDayL0 * rd.l0,
    l1: (86400 / 300) * nMetrics * bytesPerPoint * rd.l1,
    l2: (86400 / 3600) * nMetrics * bytesPerPoint * rd.l2,
  }
})
const storage = computed(() => meta.value?.storage)

// 配置导入 / 导出
function exportConfig() {
  getJSON('api/config/export').then((d) => {
    const blob = new Blob([JSON.stringify(d, null, 2)], { type: 'application/json' })
    const a = document.createElement('a')
    a.href = URL.createObjectURL(blob)
    a.download = `guanlan-config-${new Date().toISOString().slice(0, 10)}.json`
    a.click()
    URL.revokeObjectURL(a.href)
  }).catch(() => {})
}
const fileInput = ref(null)
const importMsg = ref('')
function importConfig(ev) {
  const f = ev.target.files?.[0]
  if (!f) return
  const reader = new FileReader()
  reader.onload = async () => {
    try {
      const body = JSON.parse(reader.result)
      const res = await postJSON('api/config/import', body)
      importMsg.value = t('set.importDone', { n: (res.applied || []).join(', ') })
      cfg.value = await getJSON('api/config')
    } catch (e) {
      importMsg.value = t('set.importFail')
    }
    setTimeout(() => (importMsg.value = ''), 3000)
  }
  reader.readAsText(f)
  ev.target.value = ''
}

// 历史数据导出 / 导入
function exportHistory() {
  fetch('api/history/export', { headers: { 'X-Requested-With': 'guanlan' } })
    .then((r) => {
      if (!r.ok) throw new Error(String(r.status))
      return r.blob()
    })
    .then((b) => {
      const a = document.createElement('a')
      a.href = URL.createObjectURL(b)
      a.download = `guanlan-history-${new Date().toISOString().slice(0, 10)}.db`
      a.click()
      URL.revokeObjectURL(a.href)
    })
    .catch(() => { histMsg.value = t('set.importFail') })
}
const histFile = ref(null)
const histMsg = ref('')
const histBusy = ref(false)
function importHistory(ev) {
  const f = ev.target.files?.[0]
  if (!f) return
  histBusy.value = true
  fetch('api/history/import', {
    method: 'POST',
    headers: { 'Content-Type': 'application/octet-stream', 'X-Requested-With': 'guanlan' },
    body: f,
  })
    .then(async (r) => {
      if (r.ok) {
        const d = await r.json()
        histMsg.value = t('set.histImportDone', { n: d.totalRows })
        meta.value = await getJSON('api/meta')
      } else {
        histMsg.value = t('set.histImportFail')
      }
    })
    .catch(() => { histMsg.value = t('set.histImportFail') })
    .finally(() => { histBusy.value = false; setTimeout(() => (histMsg.value = ''), 5000) })
  ev.target.value = ''
}

// 清空历史数据（模态二次确认）
const showClear = ref(false)
const clearing = ref(false)
const clearDone = ref('')
async function doClear() {
  clearing.value = true
  try {
    await postJSON('api/data/clear', { confirm: 'CLEAR' })
    meta.value = await getJSON('api/meta')
    clearDone.value = t('set.clearDone')
    showClear.value = false
  } catch (e) { /* 忽略 */ }
  clearing.value = false
  setTimeout(() => (clearDone.value = ''), 3000)
}
</script>

<template>
  <div v-if="cfg" class="settings">
    <section class="panel">
      <h3>{{ t('set.collect') }}</h3>
      <div class="row">
        <label>{{ t('ctrl.sampleInterval') }}</label>
        <select class="sel" :value="cfg.intervalMs / 1000" @change="setSample(parseFloat($event.target.value))">
          <option v-for="v in SAMPLE_CHOICES" :key="v" :value="v">{{ v }}s</option>
        </select>
      </div>
      <div class="row">
        <label>{{ t('ctrl.dbInterval') }}</label>
        <select class="sel" :value="cfg.dbIntervalMs / 1000" @change="setDb(parseInt($event.target.value))">
          <option v-for="v in DB_CHOICES" :key="v" :value="v">{{ v }}s</option>
        </select>
      </div>
      <div class="row">
        <label>{{ t('set.lang') }}</label>
        <select class="sel" :value="locale" @change="setLang($event.target.value)">
          <option value="zh">中文</option>
          <option value="en">English</option>
          <option value="ja">日本語</option>
        </select>
        <span v-if="saved" class="ok">{{ saved }}</span>
      </div>
    </section>

    <section class="panel">
      <h3>{{ t('set.retain') }}</h3>
      <div class="row" v-for="k in ['l0', 'l1', 'l2']" :key="k">
        <label>{{ t(`set.retain_${k}`) }}</label>
        <input type="number" min="1" max="3650" class="num" :value="cfg.retainDays[k]"
          @change="setRetain(k, parseInt($event.target.value) || 1)" />
        <span class="unit">{{ t('set.days') }}</span>
      </div>
      <p class="hint">{{ t('set.retainHint') }}</p>
    </section>

    <section class="panel">
      <h3>{{ t('set.storage') }}</h3>
      <template v-if="storage">
        <div class="row"><label>{{ t('set.dbSize') }}</label><b>{{ fmtBytes(storage.dbBytes || 0) }}</b></div>
        <div class="row"><label>L0 / L1 / L2</label>
          <b class="mono">{{ storage.rows.l0 }} / {{ storage.rows.l1 }} / {{ storage.rows.l2 }} {{ t('set.rows') }}</b></div>
        <template v-if="estimate">
          <div class="row"><label>{{ t('set.estL0') }}</label><b>≈ {{ fmtBytes(estimate.l0) }}</b></div>
          <div class="row"><label>{{ t('set.estL1') }}</label><b>≈ {{ fmtBytes(estimate.l1) }}</b></div>
          <div class="row"><label>{{ t('set.estL2') }}</label><b>≈ {{ fmtBytes(estimate.l2) }}</b></div>
          <p class="hint">{{ t('set.estNote') }}</p>
        </template>
      </template>
      <div class="danger-zone">
        <button class="danger-btn" @click="showClear = true">🗑 {{ t('set.clearBtn') }}</button>
        <p class="hint">{{ t('set.clearHint') }}</p>
        <span v-if="clearDone" class="ok">{{ clearDone }}</span>
      </div>
    </section>

    <section class="panel">
      <h3>{{ t('set.backup') }}</h3>
      <div class="row">
        <label>{{ t('set.configBackup') }}</label>
        <button class="btn" @click="exportConfig">{{ t('set.export') }}</button>
        <button class="btn" @click="fileInput && fileInput.click()">{{ t('set.import') }}</button>
        <input ref="fileInput" type="file" accept="application/json,.json" style="display:none" @change="importConfig" />
        <span v-if="importMsg" class="ok">{{ importMsg }}</span>
      </div>
      <div class="row"><label>{{ t('set.version') }}</label><b class="mono">{{ meta?.version || '-' }}</b></div>
    </section>

    <!-- 清空确认模态 -->
    <div v-if="showClear" class="modal-mask" @click.self="showClear = false">
      <div class="modal">
        <h4>{{ t('set.clearTitle') }}</h4>
        <p>{{ t('set.clearBody') }}</p>
        <p class="warn">{{ t('set.clearWarn') }}</p>
        <div class="modal-btns">
          <button class="btn" @click="showClear = false">{{ t('set.cancel') }}</button>
          <button class="danger-btn" :disabled="clearing" @click="doClear">
            {{ clearing ? '…' : t('set.clearConfirm') }}</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.panel { margin-top: 12px; background: var(--panel); border: 1px solid var(--border); border-radius: 10px; padding: 10px 14px; }
.panel h3 { margin: 2px 0 8px; font-size: 13px; color: var(--muted); font-weight: 500; }
.row { display: flex; align-items: center; gap: 12px; padding: 6px 0; font-size: 13px; }
.row > label { flex: none; color: var(--muted); }
.row label { color: var(--muted); }
.unit { color: var(--muted); margin-left: -6px; }
.sel { background: var(--bg); color: var(--text); border: 1px solid var(--border); border-radius: 6px; padding: 3px 10px; font-size: 13px; cursor: pointer; }
.num { background: var(--bg); color: var(--text); border: 1px solid var(--border); border-radius: 6px; padding: 3px 8px; width: 90px; font-size: 13px; }
.btn { background: var(--bg); color: var(--text); border: 1px solid var(--border); border-radius: 6px; padding: 4px 14px; font-size: 13px; cursor: pointer; }
.ok { color: var(--green); font-size: 12px; }
.hint { color: var(--muted); font-size: 12px; margin: 6px 0 0; }
.mono { font-family: ui-monospace, Consolas, monospace; }
.danger-zone { margin-top: 14px; padding-top: 12px; border-top: 1px dashed var(--border); }
.danger-btn {
  background: rgba(248, 81, 73, 0.12); color: var(--red);
  border: 1px solid var(--red); border-radius: 6px;
  padding: 6px 16px; font-size: 13px; cursor: pointer; font-weight: 600;
}
.danger-btn:hover { background: rgba(248, 81, 73, 0.25); }
.modal-mask { position: fixed; inset: 0; background: rgba(0,0,0,.55); display: flex; align-items: center; justify-content: center; z-index: 100; }
.modal { background: var(--panel); border: 1px solid var(--border); border-radius: 10px; padding: 18px 20px; max-width: 460px; }
.modal h4 { margin: 0 0 8px; font-size: 15px; }
.modal p { font-size: 13px; color: var(--text); }
.modal .warn { color: var(--red); }
.modal-btns { display: flex; justify-content: flex-end; gap: 10px; margin-top: 14px; }
</style>
