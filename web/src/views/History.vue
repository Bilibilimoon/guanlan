<script setup>
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import LiveChart from '../components/LiveChart.vue'
import { getJSON } from '../api'
import { fmtDur, tempName } from '../fmt'

const { t, locale } = useI18n()
const C = { blue: '#2f81f7', green: '#3fb950', purple: '#bc8cff', orange: '#d29922', red: '#f85149', cyan: '#39c5cf', pink: '#ff7b72', lime: '#7ee787' }
const COLORS = [C.blue, C.green, C.orange, C.purple, C.cyan, C.red, C.lime, C.pink]

// 两套窗口档：温度/网络/磁盘 = 概览的细档 + 历史页的长档（10秒~1年）；
// 占用/功耗/负载/风扇 = 只有长档（细档在这几项上看不出东西）。
// 1小时、24小时与长档的 1小时、1天是同一时段，只保留一个按钮
const FINE_RANGES = [
  { sec: 10, ns: 'win', key: 's10' },
  { sec: 30, ns: 'win', key: 's30' },
  { sec: 60, ns: 'win', key: 'm1' },
  { sec: 300, ns: 'win', key: 'm5' },
  { sec: 900, ns: 'win', key: 'm15' },
  { sec: 3600, ns: 'hist', key: 'r1h' },
  { sec: 21600, ns: 'win', key: 'h6' },
  { sec: 86400, ns: 'hist', key: 'r1d' },
  { sec: 7 * 86400, ns: 'hist', key: 'r1w' },
  { sec: 30 * 86400, ns: 'hist', key: 'r1M' },
  { sec: 365 * 86400, ns: 'hist', key: 'r1y' },
]
const COARSE_RANGES = [
  { sec: 600, ns: 'hist', key: 'r10m' },
  { sec: 3600, ns: 'hist', key: 'r1h' },
  { sec: 86400, ns: 'hist', key: 'r1d' },
  { sec: 7 * 86400, ns: 'hist', key: 'r1w' },
  { sec: 30 * 86400, ns: 'hist', key: 'r1M' },
  { sec: 365 * 86400, ns: 'hist', key: 'r1y' },
]

const GROUPS = [
  { id: 'usage', yType: 'percent', metrics: ['cpu', 'mem_pct', 'npu_pct', 'gpu_pct', 'swap_pct'] },
  { id: 'power', yType: 'watt', metrics: ['power_w'] },
  { id: 'thermal', yType: 'celsius', prefix: 'temp.', fine: true },
  { id: 'network', yType: 'bytes', prefix: 'net.', fine: true },
  { id: 'disk', yType: 'bytes', prefix: 'disk.', fine: true },
  { id: 'load', yType: 'count', metrics: ['load1', 'load5', 'load15'] },
  { id: 'fan', yType: 'rpm', metrics: ['fan'] },
]

// 各套档位各自记住上次选择；首次进入用概览的默认 15 分钟 / 历史页的默认 7 天
const fineSec = ref(900)
const coarseSec = ref(7 * 86400)
// 切档位/换层后组件会 dispatchAction 重置 dataZoom，那会合成一次 dataZoom 事件；
// 短暂忽略，避免刚切到 L1 就被这次事件按"可视跨度小"又拉回 L0
let zoomGuardUntil = 0
const group = ref('usage')
const level = ref('l0')
const series = ref([])
const resetKey = ref(0)
const loading = ref(false)

const metaRef = ref(null)
const groupDef = computed(() => GROUPS.find((g) => g.id === group.value))
const ranges = computed(() => (groupDef.value.fine ? FINE_RANGES : COARSE_RANGES))
const rangeSec = computed(() => (groupDef.value.fine ? fineSec.value : coarseSec.value))

// 滚轮缩放后把可视跨度吸附到最近档位（时间尺度上等比，所以用对数距离）
function nearestSec(span) {
  const list = ranges.value
  let best = list[0].sec
  let bestD = Infinity
  for (const r of list) {
    const d = Math.abs(Math.log(r.sec / Math.max(span, 1)))
    if (d < bestD) { bestD = d; best = r.sec }
  }
  return best
}
// 只改档位显示（下拉框），不重新取数
function setCurrentSec(sec) {
  if (groupDef.value.fine) fineSec.value = sec
  else coarseSec.value = sec
}

// 磁盘空间保护状态（meta 里 disk.paused）
const diskPaused = computed(() => metaRef.value?.disk?.paused === true)
const diskFree = computed(() => {
  const v = metaRef.value?.disk?.freeGb
  return typeof v === 'number' && v >= 0 ? v.toFixed(1) : '—'
})
const diskMin = computed(() => {
  const v = metaRef.value?.disk?.minGb
  return typeof v === 'number' ? v.toFixed(1) : '0'
})

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

// 并发保护：快速切档/滚轮换层时，旧请求返回后不得覆盖新请求的结果
let loadSeq = 0

async function loadRange(sec, lv, keepView = false) {
  const seq = ++loadSeq
  loading.value = true
  try {
    if (!metaRef.value) metaRef.value = await getJSON('api/meta')
    const end = Math.floor(Date.now() / 1000)
    const start = end - sec
    const ms = metricsOf()
    if (!ms.length) { series.value = []; return }
    const res = await getJSON(`api/series?metrics=${ms.join(',')}&start=${start}&end=${end}&level=${lv}`)
    if (seq !== loadSeq) return
    const out = []
    Object.entries(res.series || {}).forEach(([name, pts], i) => {
      if (pts && pts.length) {
        out.push({ name: prettyName(name), color: COLORS[i % COLORS.length], points: pts })
      }
    })
    series.value = out
    level.value = lv
    if (!keepView) {
      zoomGuardUntil = Date.now() + 800
      resetKey.value++
    }
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
  if (groupDef.value.fine) fineSec.value = sec
  else coarseSec.value = sec
  loadRange(sec, defaultLevel(sec))
}

// 切分组时窗口档也跟着换一套：各自用上次在该套里选的档位
function switchGroup(id) {
  group.value = id
  setRange(rangeSec.value)
}

// 滚轮缩放：吸附档位 + 按可视跨度动态换层（换层后保留可视窗口）
let zoomTimer = null
let metaTimer = null
function onViewChange(v) {
  if (!v) return
  clearTimeout(zoomTimer)
  zoomTimer = setTimeout(() => {
    if (Date.now() < zoomGuardUntil) return // 档位切换时 resetKey 触发的合成事件，忽略
    const s = v.startSec
    const e = v.endSec
    const span = e - s
    if (!isFinite(s) || !isFinite(e) || span <= 0) return
    // 下拉框跟着滚轮走：显示离可视跨度最近的档位（只改显示，不重新取数）
    setCurrentSec(nearestSec(span))
    const now = Date.now() / 1000
    const want = span <= 2 * 86400 && s > now - 30 * 86400 ? 'l0'
      : span <= 180 * 86400 && s > now - 180 * 86400 ? 'l1'
      : 'l2'
    if (want !== level.value) {
      // 请求范围覆盖可视窗口外扩 20%（至少覆盖当前窗口）
      const pad = Math.max(span * 0.2, 60)
      loadWindow(s - pad, e + pad, want)
    }
  }, 260)
}

// 缩小到当前档位全量后继续滚：自动切到下一档并加载（分页式，不打断锚点）
let beyondAt = 0
function onZoomBeyond() {
  const now = Date.now()
  if (now - beyondAt < 500) return
  beyondAt = now
  const list = ranges.value
  const idx = list.findIndex((r) => r.sec === rangeSec.value)
  if (idx < 0 || idx >= list.length - 1) return
  setRange(list[idx + 1].sec)
}

async function loadWindow(start, end, lv) {
  const seq = ++loadSeq
  loading.value = true
  try {
    const ms = metricsOf()
    if (!ms.length) return
    const res = await getJSON(`api/series?metrics=${ms.join(',')}&start=${Math.floor(start)}&end=${Math.ceil(end)}&level=${lv}`)
    if (seq !== loadSeq) return
    const out = []
    Object.entries(res.series || {}).forEach(([name, pts], i) => {
      if (pts && pts.length) out.push({ name: prettyName(name), color: COLORS[i % COLORS.length], points: pts })
    })
    series.value = out
    level.value = lv
    // 下拉框只能是档位里的值：按实际跨度吸附，避免出现"空白选中"
    setCurrentSec(nearestSec(end - start))
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  setRange(rangeSec.value)
  // meta 定期刷新：磁盘空间保护状态变了要能及时提示
  metaTimer = setInterval(async () => {
    try { metaRef.value = await getJSON('api/meta') } catch (e) { /* 忽略 */ }
  }, 60000)
})
onBeforeUnmount(() => {
  if (metaTimer) clearInterval(metaTimer)
  clearTimeout(zoomTimer)
})
</script>

<template>
  <div class="hist">
    <div class="ctrl">
      <span class="ctrl-label">{{ t('ctrl.window') }}</span>
      <select class="sel" :value="rangeSec" @change="setRange(parseInt($event.target.value, 10))">
        <option v-for="r in ranges" :key="r.sec" :value="r.sec">{{ t(`${r.ns}.${r.key}`) }}</option>
      </select>
      <div class="seg">
        <button v-for="g in GROUPS" :key="g.id" :class="['segb', { active: group === g.id }]"
          @click="switchGroup(g.id)">{{ t(`hist.group.${g.id}`) }}</button>
      </div>
      <span class="ctrl-label">{{ levelTip }}</span>
      <span v-if="loading" class="ctrl-label">…</span>
    </div>
    <p v-if="diskPaused" class="diskbar">
      {{ t('hist.diskPaused', { free: diskFree, min: diskMin }) }}
    </p>
    <section class="panel">
      <LiveChart :series="series" :y-type="groupDef.yType" height="62vh" :zoom="true" :reset-key="resetKey"
        @viewchange="onViewChange" @zoombeyond="onZoomBeyond" />
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
  display: inline-flex; flex-wrap: wrap; max-width: 100%; gap: 2px; padding: 2px;
  background: var(--bg); border: 1px solid var(--border); border-radius: 8px;
}
.sel {
  background: var(--bg); color: var(--text); border: 1px solid var(--border);
  border-radius: 6px; padding: 3px 8px; font-size: 12px; cursor: pointer;
}
.diskbar {
  margin: 10px 0 0; padding: 8px 12px; font-size: 12px; color: var(--red);
  background: rgba(248, 81, 73, 0.12); border: 1px solid var(--red); border-radius: 8px;
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
