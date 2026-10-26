<script setup>
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import LiveChart from '../components/LiveChart.vue'
import { getJSON } from '../api'
import { fmtDur, tempName } from '../fmt'

const { t, locale } = useI18n()
const C = { blue: '#2f81f7', green: '#3fb950', purple: '#bc8cff', orange: '#d29922', red: '#f85149', cyan: '#39c5cf', pink: '#ff7b72', lime: '#7ee787' }
const COLORS = [C.blue, C.green, C.orange, C.purple, C.cyan, C.red, C.lime, C.pink]

const RANGES = [
  { sec: 600, key: 'r10m' },
  { sec: 3600, key: 'r1h' },
  { sec: 86400, key: 'r1d' },
  { sec: 7 * 86400, key: 'r1w' },
  { sec: 30 * 86400, key: 'r1M' },
  { sec: 365 * 86400, key: 'r1y' },
]

const GROUPS = [
  { id: 'usage', yType: 'percent', metrics: ['cpu', 'mem_pct', 'npu_pct', 'gpu_pct', 'swap_pct'] },
  { id: 'power', yType: 'watt', metrics: ['power_w'] },
  { id: 'thermal', yType: 'celsius', prefix: 'temp.' },
  { id: 'network', yType: 'bytes', prefix: 'net.' },
  { id: 'disk', yType: 'bytes', prefix: 'disk.' },
  { id: 'load', yType: 'count', metrics: ['load1', 'load5', 'load15'] },
  { id: 'fan', yType: 'rpm', metrics: ['fan'] },
]

const rangeSec = ref(7 * 86400)
const group = ref('usage')
const level = ref('l0')
const series = ref([])
const resetKey = ref(0)
const loading = ref(false)

const metaRef = ref(null)
const groupDef = computed(() => GROUPS.find((g) => g.id === group.value))

// 组内指标清单：固定列表或按前缀从已知指标里取
function metricsOf() {
  const g = groupDef.value
  if (g.metrics) return g.metrics
  const known = metaRef.value?.metrics || []
  return known.filter((m) => m.startsWith(g.prefix) && !m.includes('boot')).slice(0, 24)
}

// 层标签
const LEVEL_LABEL = { l0: 'L0', l1: 'L1', l2: 'L2' }
const levelTip = computed(() => {
  const g = t(`hist.group.${group.value}`)
  return `${g} · ${LEVEL_LABEL[level.value]}`
})

async function loadRange(sec, lv, keepView = false) {
  loading.value = true
  try {
    if (!metaRef.value) metaRef.value = await getJSON('api/meta')
    const end = Math.floor(Date.now() / 1000)
    const start = end - sec
    const ms = metricsOf()
    if (!ms.length) { series.value = []; return }
    const res = await getJSON(`api/series?metrics=${ms.join(',')}&start=${start}&end=${end}&level=${lv}`)
    const out = []
    Object.entries(res.series || {}).forEach(([name, pts], i) => {
      if (pts && pts.length) {
        out.push({ name: prettyName(name), color: COLORS[i % COLORS.length], points: pts })
      }
    })
    series.value = out
    level.value = lv
    if (!keepView) resetKey.value++
  } finally {
    loading.value = false
  }
}

function prettyName(name) {
  if (name.startsWith('temp.')) return tempName(name.slice(5), locale.value)
  return name
    .replace('-thermal', '')
    .replace('net.', '')
    .replace('disk.', '')
    .replace('mem_pct', t('card.mem'))
    .replace('swap_pct', 'Swap')
    .replace('cpu', 'CPU')
    .replace('npu_pct', 'NPU')
    .replace('gpu_pct', 'GPU')
    .replace('load1', 'load 1m')
    .replace('load5', 'load 5m')
    .replace('load15', 'load 15m')
    .replace('power_w', t('hist.powerW'))
}

// 按当前层默认
function defaultLevel(sec) {
  if (sec <= 7 * 86400) return 'l0'
  if (sec <= 45 * 86400) return 'l1'
  return 'l2'
}

function setRange(sec) {
  rangeSec.value = sec
  loadRange(sec, defaultLevel(sec))
}

// 滚轮缩放：按可视跨度动态换层（换层后保留可视窗口）
let zoomTimer = null
function onViewChange(v) {
  if (!v || v.startValue == null) return
  clearTimeout(zoomTimer)
  zoomTimer = setTimeout(() => {
    const s = v.startValue / 1000
    const e = v.endValue / 1000
    const span = e - s
    if (span <= 0) return
    const now = Date.now() / 1000
    const want = span <= 2 * 86400 && s > now - 30 * 86400 ? 'l0'
      : span <= 180 * 86400 && s > now - 180 * 86400 ? 'l1'
      : 'l2'
    if (want !== level.value) {
      // 请求范围覆盖可视窗口外扩 20%（至少覆盖当前窗口）
      const pad = Math.max(span * 0.2, 60)
      loadWindow(s - pad, e + pad, want)
    }
  }, 500)
}

async function loadWindow(start, end, lv) {
  loading.value = true
  try {
    const ms = metricsOf()
    if (!ms.length) return
    const res = await getJSON(`api/series?metrics=${ms.join(',')}&start=${Math.floor(start)}&end=${Math.ceil(end)}&level=${lv}`)
    const out = []
    Object.entries(res.series || {}).forEach(([name, pts], i) => {
      if (pts && pts.length) out.push({ name: prettyName(name), color: COLORS[i % COLORS.length], points: pts })
    })
    series.value = out
    level.value = lv
    rangeSec.value = Math.round(end - start)
  } finally {
    loading.value = false
  }
}

onMounted(() => setRange(rangeSec.value))
</script>

<template>
  <div class="hist">
    <div class="ctrl">
      <div class="seg">
        <button v-for="r in RANGES" :key="r.sec" :class="['segb', { active: rangeSec === r.sec }]"
          @click="setRange(r.sec)">{{ t(`hist.${r.key}`) }}</button>
      </div>
      <div class="seg">
        <button v-for="g in GROUPS" :key="g.id" :class="['segb', { active: group === g.id }]"
          @click="group = g.id; setRange(rangeSec)">{{ t(`hist.group.${g.id}`) }}</button>
      </div>
      <span class="ctrl-label">{{ levelTip }}</span>
      <span v-if="loading" class="ctrl-label">…</span>
    </div>
    <section class="panel">
      <LiveChart :series="series" :y-type="groupDef.yType" height="62vh" :zoom="true" :reset-key="resetKey"
        @viewchange="onViewChange" />
    </section>
    <p class="hint">{{ t('hist.hint') }}</p>
  </div>
</template>

<style scoped>
.ctrl {
  display: flex; align-items: center; gap: 18px; flex-wrap: wrap;
  margin-top: 14px; padding: 8px 12px;
  background: var(--panel); border: 1px solid var(--border); border-radius: 10px;
}
.seg {
  display: inline-flex; gap: 2px; padding: 2px;
  background: var(--bg); border: 1px solid var(--border); border-radius: 8px;
}
.segb {
  background: none; border: none; color: var(--muted);
  padding: 4px 12px; border-radius: 6px; cursor: pointer; font-size: 12px;
  transition: background .15s, color .15s;
}
.segb:hover { color: var(--text); }
.segb.active { background: var(--accent); color: #fff; }
.ctrl-label { color: var(--muted); font-size: 12px; white-space: nowrap; }
.panel {
  margin-top: 12px; background: var(--panel); border: 1px solid var(--border);
  border-radius: 10px; padding: 10px 12px 6px;
}
.hint { color: var(--muted); font-size: 12px; }
</style>
