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

// 存储估算：行数 × 每行约 34 字节（值+主键索引页）
const estimate = computed(() => {
  if (!cfg.value || !meta.value) return null
  const dbMs = cfg.value.dbIntervalMs
  const nMetrics = Math.max((meta.value.metrics || []).length, 30)
  const bytesPerPoint = 34
  const perDayL0 = (86400 / (dbMs / 1000)) * nMetrics * bytesPerPoint
  const rd = cfg.value.retainDays
  return {
    l0Per30d: perDayL0 * rd.l0,
    l1Per180d: (86400 / 300) * nMetrics * bytesPerPoint * rd.l1,
    l2Per365d: (86400 / 3600) * nMetrics * bytesPerPoint * rd.l2,
  }
})

const storage = computed(() => meta.value?.storage)
function switchLang() {
  locale.value = locale.value === 'zh' ? 'en' : 'zh'
  localStorage.setItem('guanlan.lang', locale.value)
}
</script>

<template>
  <div v-if="cfg" class="settings">
    <section class="panel">
      <h3>{{ t('set.collect') }}</h3>
      <div class="row">
        <span>{{ t('ctrl.sampleInterval') }}</span>
        <select class="sel" :value="cfg.intervalMs / 1000" @change="setSample(parseFloat($event.target.value))">
          <option v-for="v in SAMPLE_CHOICES" :key="v" :value="v">{{ v }}s</option>
        </select>
        <span>{{ t('ctrl.dbInterval') }}</span>
        <select class="sel" :value="cfg.dbIntervalMs / 1000" @change="setDb(parseInt($event.target.value))">
          <option v-for="v in DB_CHOICES" :key="v" :value="v">{{ v }}s</option>
        </select>
        <span v-if="saved" class="ok">{{ saved }}</span>
      </div>
    </section>

    <section class="panel">
      <h3>{{ t('set.retain') }}</h3>
      <div class="row" v-for="k in ['l0', 'l1', 'l2']" :key="k">
        <span>{{ t(`set.retain_${k}`) }}</span>
        <input type="number" min="1" max="3650" class="num" :value="cfg.retainDays[k]"
          @change="setRetain(k, parseInt($event.target.value) || 1)" />
        <span>{{ t('set.days') }}</span>
      </div>
      <p class="hint">{{ t('set.retainHint') }}</p>
    </section>

    <section class="panel">
      <h3>{{ t('set.storage') }}</h3>
      <template v-if="storage">
        <div class="row"><span>{{ t('set.dbSize') }}</span><b>{{ fmtBytes(storage.dbBytes || 0) }}</b></div>
        <div class="row"><span>L0 / L1 / L2</span>
          <b class="mono">{{ storage.rows.l0 }} / {{ storage.rows.l1 }} / {{ storage.rows.l2 }} {{ t('set.rows') }}</b></div>
        <template v-if="estimate">
          <div class="row"><span>{{ t('set.estL0') }}</span><b>≈ {{ fmtBytes(estimate.l0Per30d) }}</b></div>
          <div class="row"><span>{{ t('set.estL1') }}</span><b>≈ {{ fmtBytes(estimate.l1Per180d) }}</b></div>
          <div class="row"><span>{{ t('set.estL2') }}</span><b>≈ {{ fmtBytes(estimate.l2Per365d) }}</b></div>
          <p class="hint">{{ t('set.estNote') }}</p>
        </template>
      </template>
    </section>

    <section class="panel">
      <h3>{{ t('set.misc') }}</h3>
      <div class="row">
        <span>{{ t('set.lang') }}</span>
        <button class="sel" @click="switchLang">{{ locale === 'zh' ? 'English' : '中文' }}</button>
      </div>
      <div class="row"><span>{{ t('set.version') }}</span><b class="mono">{{ meta?.version || '-' }}</b></div>
    </section>
  </div>
</template>

<style scoped>
.panel { margin-top: 12px; background: var(--panel); border: 1px solid var(--border); border-radius: 10px; padding: 10px 14px; }
.panel h3 { margin: 2px 0 8px; font-size: 13px; color: var(--muted); font-weight: 500; }
.row { display: flex; align-items: center; gap: 10px; padding: 6px 0; font-size: 13px; flex-wrap: wrap; }
.row span { color: var(--muted); min-width: 150px; }
.sel { background: var(--bg); color: var(--text); border: 1px solid var(--border); border-radius: 6px; padding: 3px 10px; font-size: 13px; cursor: pointer; }
.num { background: var(--bg); color: var(--text); border: 1px solid var(--border); border-radius: 6px; padding: 3px 8px; width: 90px; font-size: 13px; }
.ok { color: var(--green); font-size: 12px; }
.hint { color: var(--muted); font-size: 12px; }
.mono { font-family: ui-monospace, Consolas, monospace; }
</style>
