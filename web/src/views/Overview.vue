<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import StatCard from '../components/StatCard.vue'
import LiveChart from '../components/LiveChart.vue'
import { getJSON, postJSON, poll } from '../api'
import { fmtBytes, fmtBps, fmtGiB, fmtPct, fmtC, fmtUptime, fmtDur, pressureColor, tempName, tempTip } from '../fmt'

const { t, locale } = useI18n()
const C = { blue: '#2f81f7', green: '#3fb950', purple: '#bc8cff', orange: '#d29922', red: '#f85149', cyan: '#39c5cf' }

// ---- 控制条状态 ----
const WINDOWS = [
  { sec: 10, label: () => t('win.s10') },
  { sec: 30, label: () => t('win.s30') },
  { sec: 60, label: () => t('win.m1') },
  { sec: 300, label: () => t('win.m5') },
  { sec: 900, label: () => t('win.m15') },
  { sec: 3600, label: () => t('win.h1') },
  { sec: 21600, label: () => t('win.h6') },
  { sec: 86400, label: () => t('win.h24') },
]
const RING_MAX_SEC = 900
const windowSec = ref(900)

const SAMPLE_CHOICES = [1, 2, 5, 10, 15] // 秒
const DB_CHOICES = [5, 10, 15, 30, 60]   // 秒
const sampleSec = ref(2)
const dbSec = ref(15)
const cfgSaved = ref('')

function nearestChoice(v, list) {
  return list.reduce((a, b) => (Math.abs(b - v) < Math.abs(a - v) ? b : a))
}

// ---- 数据 ----
// 每个图表面板的时间窗口 = 基准窗口 × 宽度档位系数（1列×1、2列×2、整行×4），
// 因此数据按"窗口秒数"分桶存放
const rowsByWindow = ref({}) // windowSec -> 行数组
const latest = ref(null)     // 最新完整样本（卡片用）
const metaRef = ref(null)    // /api/meta（网卡 IP、公网 IP 等）

function makeRow() {
  return { t: 0, cpu: 0, memPct: 0, npuPct: 0, gpuPct: 0, temps: {}, net: {}, disks: {} }
}

function sampleToRow(s) {
  const r = Object.assign(makeRow(), {
    t: s.t, cpu: s.cpu, npuPct: s.npuPct, gpuPct: s.gpuPct,
    temps: s.temps || {}, net: s.net || {}, disks: s.disks || {},
  })
  r.memPct = s.memTotal ? (s.memUsed / s.memTotal) * 100 : 0
  return r
}

function pushRow(r) {
  if (!r || !r.t) return
  for (const w of Object.keys(rowsByWindow.value)) {
    const arr = rowsByWindow.value[w]
    if (arr.length && r.t <= arr[arr.length - 1].t) continue
    arr.push(r)
    const cutoff = r.t - Number(w)
    while (arr.length && arr[0].t < cutoff) arr.shift()
  }
}

function seriesToRows(seriesObj) {
  const map = new Map()
  const ensure = (ts) => {
    let r = map.get(ts)
    if (!r) { r = makeRow(); r.t = ts; map.set(ts, r) }
    return r
  }
  for (const [name, pts] of Object.entries(seriesObj || {})) {
    for (const [ts, v] of pts) {
      const r = ensure(ts)
      if (name === 'cpu') r.cpu = v
      else if (name === 'mem_pct') r.memPct = v
      else if (name === 'npu_pct') r.npuPct = v
      else if (name === 'gpu_pct') r.gpuPct = v
      else if (name.startsWith('temp.')) r.temps[name.slice(5)] = v
      else if (name.startsWith('net.')) {
        const parts = name.split('.')
        const cell = (r.net[parts[1]] ||= {})
        cell[parts[2] === 'rx' ? 'rxRate' : 'txRate'] = v
      } else if (name.startsWith('disk.')) {
        const parts = name.split('.')
        const cell = (r.disks[parts[1]] ||= {})
        cell[parts[2] === 'r' ? 'readBps' : 'writeBps'] = v
      }
    }
  }
  return [...map.values()].sort((a, b) => a.t - b.t)
}

async function loadRealtime() {
  try { latest.value = await getJSON('api/realtime') } catch (e) { /* 忽略 */ }
}

async function loadMeta() {
  try { metaRef.value = await getJSON('api/meta') } catch (e) { /* 忽略 */ }
}

async function loadWindow(w) {
  const end = Math.floor(Date.now() / 1000)
  try {
    if (w <= RING_MAX_SEC) {
      const ring = await getJSON(`api/ring?sec=${w}`)
      rowsByWindow.value = { ...rowsByWindow.value, [w]: ring.map(sampleToRow) }
    } else {
      const m = ['cpu', 'mem_pct', 'npu_pct', 'gpu_pct']
      const lm = latest.value
      if (lm) {
        for (const k of Object.keys(lm.temps || {})) m.push('temp.' + k)
        const ifc = activeIfaceOf(lm)
        if (ifc) m.push(`net.${ifc}.rx`, `net.${ifc}.tx`)
        for (const d of Object.keys(lm.disks || {})) m.push(`disk.${d}.r`, `disk.${d}.w`)
      }
      const res = await getJSON(`api/series?metrics=${m.join(',')}&start=${end - w}&end=${end}&level=l0`)
      rowsByWindow.value = { ...rowsByWindow.value, [w]: seriesToRows(res.series) }
    }
  } catch (e) { /* 后端可能尚无历史数据 */ }
}

let stopFast = null
let stopSlow = null
let stopMeta = null
function stopAll() {
  if (stopFast) { stopFast(); stopFast = null }
  if (stopSlow) { stopSlow(); stopSlow = null }
}

// 面板窗口 = 基准窗口 × 宽度档位系数
const SPAN_FACTOR = { 1: 1, 2: 2, 3: 4 }
function panelWindowOf(pid) {
  return windowSec.value * (SPAN_FACTOR[panelSpan.value[pid] || 1] || 1)
}
function usedWindows() {
  const set = new Set([windowSec.value])
  for (const pid of panelOrder.value) set.add(panelWindowOf(pid))
  return set
}

async function reload() {
  await loadRealtime()
  // 清掉不再使用的窗口桶
  const keep = {}
  for (const w of usedWindows()) keep[w] = rowsByWindow.value[w] || []
  rowsByWindow.value = keep
  for (const w of usedWindows()) await loadWindow(w)
  stopAll()
  stopFast = poll(async () => {
    try {
      const sm = await getJSON('api/realtime')
      latest.value = sm
      pushRow(sampleToRow(sm))
    } catch (e) { /* 忽略 */ }
  }, Math.max(1000, sampleSec.value * 1000))
  if ([...usedWindows()].some((w) => w > RING_MAX_SEC)) {
    stopSlow = poll(async () => {
      for (const w of usedWindows()) if (w > RING_MAX_SEC) await loadWindow(w)
    }, 30000)
  }
}

onMounted(async () => {
  try {
    const cfg = await getJSON('api/config')
    const sec = nearestChoice(cfg.intervalMs / 1000, SAMPLE_CHOICES)
    if (sec !== cfg.intervalMs / 1000) {
      // 旧版本可能存了已移除的 0.1/0.5s：归一化并写回，保持前后端一致
      await postJSON('api/config', { intervalMs: sec * 1000 }).catch(() => {})
    }
    sampleSec.value = sec
    dbSec.value = cfg.dbIntervalMs / 1000
  } catch (e) { /* 默认值 */ }
  stopMeta = poll(loadMeta, 600000)
  await reload()
})

onBeforeUnmount(() => {
  stopAll()
  if (stopMeta) stopMeta()
})

watch(windowSec, reload)

async function applyConfig(patch) {
  try {
    await postJSON('api/config', patch)
    cfgSaved.value = t('ctrl.saved')
    setTimeout(() => (cfgSaved.value = ''), 1500)
  } catch (e) { /* 失败保留原值 */ }
}

function setSampleSec(v) {
  sampleSec.value = v
  applyConfig({ intervalMs: Math.round(v * 1000) })
  reload()
}
function setDbSec(v) {
  dbSec.value = v
  applyConfig({ dbIntervalMs: v * 1000 })
}

// ---- 拖拽排序（指针驱动，iOS 式：拖动中其他元素实时让位）----
const DEFAULT_CARD_ORDER = ['cpu', 'mem', 'swap', 'npu', 'gpu', 'temp', 'fan', 'net', 'diskio', 'power', 'load', 'procs', 'uptime']
const DEFAULT_PANEL_ORDER = ['usage', 'thermal', 'netRate', 'diskRate']

function loadJSON(key, fallback) {
  try {
    const v = JSON.parse(localStorage.getItem(key))
    return v ?? fallback
  } catch (e) { return fallback }
}
// 旧版 localStorage 存的顺序不含后来新增的卡片/面板，按默认顺序补齐，
// 否则新卡片会一直不出现（v-for 只遍历存下来的 id）
function mergeOrder(saved, defaults) {
  if (!Array.isArray(saved)) return [...defaults]
  const have = new Set(saved)
  return [...saved, ...defaults.filter((id) => !have.has(id))]
}
const cardOrder = ref(mergeOrder(loadJSON('guanlan.cardOrder', null), DEFAULT_CARD_ORDER))
const panelOrder = ref(mergeOrder(loadJSON('guanlan.panelOrder', null), DEFAULT_PANEL_ORDER))
const panelH = ref(loadJSON('guanlan.panelH', {}))
const panelSpan = ref(loadJSON('guanlan.panelSpan', {}))

// FLIP 已被拖动方案的 transform 虚拟重排取代，这里不再需要 DOM 重排动画
let drag = null
// { list, el, ghost, els, rects, fromSlot, cur, virt, grabDx, grabDy, w, h }

function dragItems(list) {
  const sel = list === 'cards' ? '.cards .card-wrap' : '.charts .panel'
  return [...document.querySelectorAll(`.overview ${sel}`)]
}

function startDrag(list, i, e) {
  if (e.button !== undefined && e.button !== 0) return
  const wrap = e.target.closest('.card-wrap, .panel')
  if (!wrap) return
  e.preventDefault()
  const rect = wrap.getBoundingClientRect()

  // 幽灵：跟随指针的克隆（canvas 位图不会随 cloneNode 复制，需手动画入）
  const ghost = wrap.cloneNode(true)
  ghost.classList.add('drag-ghost')
  ghost.style.cssText += `;position:fixed;left:${rect.left}px;top:${rect.top}px;width:${rect.width}px;height:${rect.height}px;margin:0;z-index:999;pointer-events:none;`
  const srcCanvases = wrap.querySelectorAll('canvas')
  ghost.querySelectorAll('canvas').forEach((c, idx) => {
    const src = srcCanvases[idx]
    if (src) {
      c.width = src.width
      c.height = src.height
      c.getContext('2d').drawImage(src, 0, 0)
    }
  })
  document.body.appendChild(ghost)
  wrap.classList.add('dragging')
  document.body.classList.add('guanlan-dragging')

  const els = dragItems(list)
  const rects = els.map((el) => el.getBoundingClientRect()) // 布局快照（拖动全程 DOM 不动，快照始终有效）
  drag = {
    list, el: wrap, ghost, els, rects,
    fromSlot: i, cur: i, virt: [...els],
    grabDx: e.clientX - rect.left, grabDy: e.clientY - rect.top,
    w: rect.width, h: rect.height,
  }
  moveGhost(e.clientX, e.clientY)

  const move = (ev) => {
    moveGhost(ev.clientX, ev.clientY)
    // 以幽灵中心做命中判定：中心压过槽间缝隙进入哪格，哪格让位
    const cx = ev.clientX - drag.grabDx + drag.w / 2
    const cy = ev.clientY - drag.grabDy + drag.h / 2
    const idx = hitSlot(cx, cy)
    if (idx >= 0 && idx !== drag.cur) swapTo(idx)
  }
  const up = () => {
    document.removeEventListener('pointermove', move)
    endDrag()
  }
  document.addEventListener('pointermove', move)
  document.addEventListener('pointerup', up, { once: true })
}

function moveGhost(x, y) {
  if (!drag) return
  drag.ghost.style.left = x - drag.grabDx + 'px'
  drag.ghost.style.top = y - drag.grabDy + 'px'
}

// 命中：中心点所在槽（矩形包含；缝隙时取最近槽）
function hitSlot(cx, cy) {
  let best = -1
  let bd = Infinity
  drag.rects.forEach((r, idx) => {
    if (cx >= r.left && cx <= r.right && cy >= r.top && cy <= r.bottom) { best = idx; bd = 0; return }
    const dx = Math.max(0, Math.abs(cx - (r.left + r.right) / 2) - r.width / 2)
    const dy = Math.max(0, Math.abs(cy - (r.top + r.bottom) / 2) - r.height / 2)
    const d = dx * dx + dy * dy
    if (d < bd) { bd = d; best = idx }
  })
  return best
}

// 让位：不动 DOM、不动数组，只给每个元素设置"虚拟槽位"的 transform，
// 由 CSS transition 平滑呈现（无 FLIP 测量误差，连续交换不抖）
function swapTo(idx) {
  drag.cur = idx
  const virt = [...drag.els]
  virt.splice(drag.fromSlot, 1)
  virt.splice(drag.cur, 0, drag.el)
  drag.virt = virt
  drag.els.forEach((el, slot) => {
    const v = virt.indexOf(el)
    const dx = drag.rects[v].left - drag.rects[slot].left
    const dy = drag.rects[v].top - drag.rects[slot].top
    el.style.transition = 'transform .22s cubic-bezier(.2,.8,.3,1)'
    el.style.transform = dx || dy ? `translate(${dx}px, ${dy}px)` : ''
  })
}

function endDrag() {
  const d = drag
  drag = null
  if (!d) return
  document.body.classList.remove('guanlan-dragging')
  d.el.classList.remove('dragging')

  // 提交虚拟顺序到数组（DOM 随之重排）
  const ids = d.virt.map((el) => el.dataset.id)
  if (d.list === 'cards') cardOrder.value = ids
  else panelOrder.value = ids
  const key = d.list === 'cards' ? 'guanlan.cardOrder' : 'guanlan.panelOrder'
  localStorage.setItem(key, JSON.stringify(ids))

  nextTick(() => {
    // 真实元素无动画瞬间落位（此时被最上层的幽灵盖住，不可见）
    d.els.forEach((el) => {
      el.style.transition = 'none'
      el.style.transform = ''
    })
    void d.el.offsetWidth

    // 幽灵从当前位置吸附滑向目标槽（宽高一并对齐）
    const target = d.el.getBoundingClientRect()
    const ease = 'cubic-bezier(.2,.8,.3,1)'
    d.ghost.style.transition = `left .22s ${ease}, top .22s ${ease}, width .22s ${ease}, height .22s ${ease}`
    d.ghost.style.left = target.left + 'px'
    d.ghost.style.top = target.top + 'px'
    d.ghost.style.width = target.width + 'px'
    d.ghost.style.height = target.height + 'px'

    let done = false
    const cleanup = () => {
      if (done) return
      done = true
      d.ghost.remove()
      d.els.forEach((el) => { el.style.transition = '' })
    }
    d.ghost.addEventListener('transitionend', cleanup, { once: true })
    setTimeout(cleanup, 320) // 兜底：确保幽灵最终被移除
  })
}

function resetLayout() {
  cardOrder.value = [...DEFAULT_CARD_ORDER]
  panelOrder.value = [...DEFAULT_PANEL_ORDER]
  panelH.value = {}
  panelSpan.value = {}
  localStorage.removeItem('guanlan.cardOrder')
  localStorage.removeItem('guanlan.panelOrder')
  localStorage.removeItem('guanlan.panelH')
  localStorage.removeItem('guanlan.panelSpan')
}

// ---- 面板尺寸拖拽（Windows 式把手：纵向改高度，横向按档位加宽 1列→2列→整行）----
const SPAN_SEQ = [1, 2, 3] // 3 = 整行

function startResize(pid, e) {
  e.preventDefault()
  e.stopPropagation()
  const startY = e.clientY
  const startX = e.clientX
  const startH = panelH.value[pid] || 260
  const startIdx = SPAN_SEQ.indexOf(panelSpan.value[pid] || 1)
  const STEP = 160 // 每横向拖 160px 切一档
  const move = (ev) => {
    const h = Math.min(900, Math.max(140, startH + (ev.clientY - startY)))
    panelH.value = { ...panelH.value, [pid]: h }
    const idx = Math.min(SPAN_SEQ.length - 1, Math.max(0, startIdx + Math.round((ev.clientX - startX) / STEP)))
    panelSpan.value = { ...panelSpan.value, [pid]: SPAN_SEQ[idx] }
  }
  const up = () => {
    document.removeEventListener('pointermove', move)
    document.removeEventListener('pointerup', up)
    localStorage.setItem('guanlan.panelH', JSON.stringify(panelH.value))
    localStorage.setItem('guanlan.panelSpan', JSON.stringify(panelSpan.value))
  }
  document.addEventListener('pointermove', move)
  document.addEventListener('pointerup', up)
}

function spanClass(pid) {
  const s = panelSpan.value[pid] || 1
  if (s === 3) return 'span-full'
  if (s === 2) return 'span-2'
  return ''
}

// 宽度档位变化后各面板的时间窗口跟着变，需要重新取数（放这里：panelSpan 已声明）
watch(panelSpan, reload)

// ---- 派生 ----
function activeIfaceOf(sm) {
  if (!sm || !sm.net) return null
  let best = null, bestRate = -1
  for (const [ifc, v] of Object.entries(sm.net)) {
    const r = (v.rxRate || 0) + (v.txRate || 0)
    if (r > bestRate) { bestRate = r; best = ifc }
  }
  return best ?? Object.keys(sm.net)[0] ?? null
}
const activeIface = computed(() => activeIfaceOf(latest.value))

const maxTemp = computed(() => {
  const sm = latest.value
  if (!sm) return null
  let key = null, v = -Infinity
  for (const [k, tv] of Object.entries(sm.temps || {})) if (tv > v) { v = tv; key = k }
  return v > -Infinity ? { key, v } : null
})

function buildUsage(s) {
  if (!s.length) return []
  return [
    { name: 'CPU', color: C.blue, points: s.map((x) => [x.t, x.cpu]) },
    { name: t('card.mem'), color: C.green, points: s.map((x) => [x.t, x.memPct]) },
    { name: 'NPU', color: C.purple, points: s.map((x) => [x.t, x.npuPct]) },
    { name: 'GPU', color: C.orange, points: s.map((x) => [x.t, x.gpuPct]) },
  ]
}

const thermalKeys = ['package-thermal', 'gpu-thermal', 'npu-thermal', 'sda']
const thermalColors = [C.red, C.orange, C.purple, C.cyan]
function buildThermal(s) {
  if (!s.length) return []
  return thermalKeys
    .filter((k) => s.some((x) => x.temps && x.temps[k] != null))
    .map((k, i) => ({
      name: tempName(k, locale.value),
      color: thermalColors[i],
      points: s.filter((x) => x.temps && x.temps[k] != null).map((x) => [x.t, x.temps[k]]),
    }))
}

function buildNet(s, ifc) {
  if (!s.length || !ifc) return []
  const pick = (f) => s.filter((x) => x.net && x.net[ifc]).map((x) => [x.t, f(x.net[ifc])])
  return [
    { name: t('misc.tx'), color: C.green, points: pick((v) => v.txRate || 0) },
    { name: t('misc.rx'), color: C.blue, points: pick((v) => v.rxRate || 0) },
  ]
}

function buildDisk(s) {
  if (!s.length) return []
  const out = []
  const lastRow = [...s].reverse().find((x) => Object.keys(x.disks || {}).length)
  for (const d of Object.keys((lastRow || {}).disks || {})) {
    const pick = (f) => s.filter((x) => x.disks && x.disks[d]).map((x) => [x.t, f(x.disks[d])])
    out.push({ name: `${d} ${t('misc.read')}`, color: C.green, points: pick((v) => v.readBps || 0) })
    out.push({ name: `${d} ${t('misc.write')}`, color: C.orange, points: pick((v) => v.writeBps || 0) })
  }
  return out.slice(0, 6)
}

const PANEL_META = {
  usage: { titleKey: 'chart.usage', yType: 'percent' },
  thermal: { titleKey: 'chart.thermal', yType: 'celsius' },
  netRate: { titleKey: 'chart.netRate', yType: 'bytes' },
  diskRate: { titleKey: 'chart.diskRate', yType: 'bytes' },
}
const BUILDERS = {
  usage: buildUsage, thermal: buildThermal,
  netRate: (rows, ifc) => buildNet(rows, ifc), diskRate: buildDisk,
}
const panelSeries = computed(() => {
  const ifc = activeIface.value
  const out = {}
  for (const pid of panelOrder.value) {
    const rows = rowsByWindow.value[panelWindowOf(pid)] || []
    out[pid] = BUILDERS[pid](rows, ifc)
  }
  return out
})

function chartTitle(pid) {
  return `${t(PANEL_META[pid].titleKey)} · ${fmtDur(panelWindowOf(pid), locale.value)}`
}

// ---- IP 显示 ----
const lanIPEntry = computed(() => {
  const m = metaRef.value
  if (!m || !activeIface.value) return null
  return (m.lanIPs || []).find((e) => e.iface === activeIface.value) || null
})

// ---- 卡片 ----
const cards = computed(() => {
  const sm = latest.value
  if (!sm) return {}
  const netV = sm.net ? sm.net[activeIface.value] : null
  const m = metaRef.value
  const ipLines = []
  if (m) {
    ipLines.push(`IPv4  ${lanIPEntry.value?.v4 || '-'}`)
    ipLines.push(`IPv4  ${t('misc.wan')}  ${m.publicIPv4 || '-'}`)
    ipLines.push(`IPv6  ${lanIPEntry.value?.v6 || '-'}`)
    ipLines.push(`IPv6  ${t('misc.wan')}  ${m.publicIPv6 || '-'}`)
  }
  return {
    cpu: {
      title: t('card.cpu'), value: fmtPct(sm.cpu),
      sub: t('misc.ofCores', { n: (sm.cpuCores || []).length }),
      bar: sm.cpu, color: pressureColor(sm.cpu),
    },
    mem: {
      title: t('card.mem'), value: fmtPct(sm.memTotal ? (sm.memUsed / sm.memTotal) * 100 : 0),
      sub: `${fmtGiB(sm.memUsed)} / ${fmtGiB(sm.memTotal)}`,
      bar: sm.memTotal ? (sm.memUsed / sm.memTotal) * 100 : 0,
      color: pressureColor(sm.memTotal ? (sm.memUsed / sm.memTotal) * 100 : 0),
    },
    swap: {
      title: t('card.swap'), value: sm.swapTotal ? fmtPct((sm.swapUsed / sm.swapTotal) * 100) : '-',
      sub: `${fmtGiB(sm.swapUsed)} / ${fmtGiB(sm.swapTotal)}`,
      bar: sm.swapTotal ? (sm.swapUsed / sm.swapTotal) * 100 : 0,
      color: pressureColor(sm.swapTotal ? (sm.swapUsed / sm.swapTotal) * 100 : 0),
    },
    npu: {
      title: t('card.npu'), value: fmtPct(sm.npuPct),
      sub: t('misc.freqNow', { mhz: Math.round(sm.npuMHz) }),
      bar: sm.npuPct, color: pressureColor(sm.npuPct),
    },
    gpu: {
      title: t('card.gpu'), value: fmtPct(sm.gpuPct),
      sub: t('misc.freqNow', { mhz: Math.round(sm.gpuMHz) }),
      bar: sm.gpuPct, color: pressureColor(sm.gpuPct),
    },
    temp: {
      title: t('card.temp'), value: maxTemp.value ? fmtC(maxTemp.value.v) : '-',
      sub: maxTemp.value ? tempName(maxTemp.value.key, locale.value) : '',
      bar: maxTemp.value ? Math.min(100, Math.max(0, ((maxTemp.value.v - 35) / 50) * 100)) : null,
      color: pressureColor(maxTemp.value ? ((maxTemp.value.v - 35) / 50) * 100 : 0),
    },
    fan: { title: t('card.fan'), value: `${sm.fan} RPM`, color: C.cyan },
    net: {
      title: t('card.net'),
      value: netV ? fmtBps((netV.rxRate || 0) + (netV.txRate || 0)) : '-',
      sub: netV ? `↓${fmtBps(netV.rxRate || 0)} ↑${fmtBps(netV.txRate || 0)} · ${activeIface.value}` : '',
      subs: ipLines,
      color: C.blue,
    },
    diskio: (() => {
      const d = sm.disks && sm.disks.sda ? sm.disks.sda : Object.values(sm.disks || {})[0]
      return {
        title: t('card.diskIo'),
        value: d ? fmtBps((d.readBps || 0) + (d.writeBps || 0)) : '-',
        sub: d ? `R ${fmtBps(d.readBps || 0)} · W ${fmtBps(d.writeBps || 0)}` : '',
        color: C.green,
      }
    })(),
    power: {
      title: t('card.power'),
      value: `≈ ${(sm.powerW || 0).toFixed(1)} W`,
      sub: t('misc.powerEst'),
      bar: Math.min(100, Math.max(0, (((sm.powerW || 0) - 5) / 13) * 100)),
      color: C.orange,
    },
    load: {
      title: t('card.load'), value: sm.load1.toFixed(2),
      sub: t('misc.loadHint', { n: (sm.cpuCores || []).length || 8 }),
      color: pressureColor((sm.load1 / ((sm.cpuCores || []).length || 8)) * 100),
    },
    procs: { title: t('card.procs'), value: String(sm.procs), color: C.blue },
    uptime: { title: t('card.uptime'), value: fmtUptime(sm.uptime, locale.value), color: C.cyan },
  }
})
</script>

<template>
  <div class="overview">
    <!-- 控制条 -->
    <div class="ctrl">
      <div class="ctrl-group">
        <span class="ctrl-label">{{ t('ctrl.window') }}</span>
        <select class="sel" :value="windowSec" @change="windowSec = parseInt($event.target.value, 10)">
          <option v-for="w in WINDOWS" :key="w.sec" :value="w.sec">{{ w.label() }}</option>
        </select>
      </div>
      <div class="ctrl-group">
        <span class="ctrl-label">{{ t('ctrl.sampleInterval') }}</span>
        <select class="sel" :value="sampleSec" @change="setSampleSec(parseFloat($event.target.value))">
          <option v-for="v in SAMPLE_CHOICES" :key="v" :value="v">{{ v }}s</option>
        </select>
      </div>
      <div class="ctrl-group">
        <span class="ctrl-label">{{ t('ctrl.dbInterval') }}</span>
        <select class="sel" :value="dbSec" @change="setDbSec(parseInt($event.target.value))">
          <option v-for="v in DB_CHOICES" :key="v" :value="v">{{ v }}s</option>
        </select>
      </div>
      <span v-if="cfgSaved" class="saved">{{ cfgSaved }}</span>
      <button class="reset-btn" :title="t('ctrl.resetLayoutTip')" @click="resetLayout">↺ {{ t('ctrl.resetLayout') }}</button>
    </div>

    <!-- 卡片（拖 ⠿ 排序） -->
    <div class="cards">
      <div v-for="(cid, i) in cardOrder" :key="cid" :data-id="cid"
        :class="['card-wrap', { wide: cid === 'net' }]" :title="cid === 'temp' ? tempTip(locale.value) : cid === 'power' ? t('misc.powerEstTip') : undefined">
        <span class="grip" :title="t('ctrl.dragTip')"
          @pointerdown="startDrag('cards', i, $event)">&nbsp;&#10625;&nbsp;</span>
        <StatCard v-if="cards[cid]" v-bind="cards[cid]" />
      </div>
    </div>

    <!-- 图表面板（拖 ⠿ 排序，右下角把手调高度） -->
    <div class="charts">
      <section v-for="(pid, i) in panelOrder" :key="pid" :data-id="pid" :class="['panel', spanClass(pid)]">
        <div class="panel-head">
          <span class="grip" :title="t('ctrl.dragTip')"
            @pointerdown="startDrag('panels', i, $event)">&#10625;</span>
          <h3>{{ chartTitle(pid) }}</h3>
        </div>
        <LiveChart :series="panelSeries[pid]" :y-type="PANEL_META[pid].yType"
          :height="(panelH[pid] || 260) + 'px'" />
        <span class="rs" :title="t('ctrl.resizeTip')" @pointerdown="startResize(pid, $event)"></span>
      </section>
    </div>
  </div>
</template>

<style scoped>
.ctrl {
  display: flex; align-items: center; gap: 20px; flex-wrap: wrap;
  margin-top: 14px; padding: 8px 12px;
  background: var(--panel); border: 1px solid var(--border); border-radius: 10px;
}
.ctrl-group { display: flex; align-items: center; gap: 6px; }
.ctrl-label { color: var(--muted); font-size: 12px; white-space: nowrap; }
.sel {
  background: var(--bg); color: var(--text); border: 1px solid var(--border);
  border-radius: 6px; padding: 3px 8px; font-size: 12px; cursor: pointer;
}
.saved { color: var(--green); font-size: 12px; }
.reset-btn {
  margin-left: auto; background: none; color: var(--muted);
  border: 1px solid var(--border); border-radius: 6px; padding: 3px 10px;
  font-size: 12px; cursor: pointer;
}
.reset-btn:hover { color: var(--text); }

.cards {
  display: grid; gap: 10px;
  grid-template-columns: repeat(auto-fill, minmax(210px, 1fr));
  margin-top: 12px;
}
.card-wrap { position: relative; min-width: 0; align-self: start; }
.card-wrap.wide { grid-column: span 2; }
.grip {
  position: absolute; top: 2px; left: 4px; z-index: 2;
  color: var(--muted); cursor: grab; font-size: 12px; line-height: 1;
  opacity: 0; transition: opacity 0.15s; user-select: none; touch-action: none;
}
.card-wrap:hover .grip, .panel:hover .grip { opacity: 0.7; }
.grip:active { cursor: grabbing; }
.card-wrap :deep(.card) { padding-left: 20px; }
.dragging { opacity: 0.25 !important; }
.drag-ghost {
  opacity: 0.92;
  box-shadow: 0 10px 32px rgba(0, 0, 0, 0.5);
  border-radius: 10px;
  transform: scale(1.03);
}
:global(body.guanlan-dragging) { user-select: none; cursor: grabbing; }

.charts {
  display: grid; gap: 12px; margin-top: 12px;
  grid-template-columns: repeat(auto-fit, minmax(480px, 1fr));
}
@media (max-width: 540px) { .charts { grid-template-columns: 1fr; } }
.panel {
  position: relative;
  background: var(--panel); border: 1px solid var(--border);
  border-radius: 10px; padding: 10px 12px 6px; min-width: 0;
}
.span-2 { grid-column: span 2; }
.span-full { grid-column: 1 / -1; }
.panel-head { display: flex; align-items: center; gap: 6px; }
.panel-head .grip { position: static; padding: 2px; }
.panel-head h3 { margin: 2px 0 6px; font-size: 13px; color: var(--muted); font-weight: 500; }
.rs {
  position: absolute; right: 0; bottom: 0; width: 18px; height: 18px;
  cursor: nwse-resize; opacity: 0;
  background:
    linear-gradient(135deg, transparent 50%, var(--muted) 50%) no-repeat 100% 100% / 8px 8px,
    linear-gradient(135deg, transparent 65%, var(--muted) 65%) no-repeat 100% 100% / 14px 14px;
}
.panel:hover .rs { opacity: 0.6; }
</style>
