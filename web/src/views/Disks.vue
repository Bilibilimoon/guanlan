<script setup>
import { computed, onMounted, onBeforeUnmount, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import LiveChart from '../components/LiveChart.vue'
import { getJSON, poll } from '../api'
import { fmtBytes, fmtBps, fmtC, fmtDur, pressureColor } from '../fmt'
import { useColResize } from '../colresize'

const { t, locale } = useI18n()
const HCOLS = [{ key: 'dev', w: 100 }, { key: 'io', w: 110 }, { key: 'r', w: 100 }, { key: 'w', w: 100 }, { key: 'temp', w: 90 }, { key: 'ioerr', w: 80 }, { key: 'state', w: 110 }]
const { widths: hcw, thResize: hcR, thCursor: hcCursor } = useColResize('diskHealth', Object.fromEntries(HCOLS.map((c) => [c.key, c.w])))
const diskMinW = HCOLS.reduce((a, c) => a + c.w, 0) - HCOLS[HCOLS.length - 1].w + 140
const latest = ref(null)
const rows = ref([])
let stop = null

const WINDOWS = [900, 3600, 21600, 86400]
const winSec = ref(900)

async function reload() {
  try {
    if (!latest.value) latest.value = await getJSON('api/realtime')
    if (winSec.value <= 900) {
      rows.value = await getJSON(`api/ring?sec=${winSec.value}`)
    } else {
      const end = Math.floor(Date.now() / 1000)
      const ms = []
      for (const d of Object.keys(latest.value.disks || {})) ms.push(`disk.${d}.r`, `disk.${d}.w`)
      const res = await getJSON(`api/series?metrics=${ms.slice(0, 24).join(',')}&start=${end - winSec.value}&end=${end}&level=l0`)
      const map = new Map()
      for (const [name, pts] of Object.entries(res.series || {})) {
        const [, d, rw] = name.split('.')
        for (const [ts, v] of pts) {
          let r = map.get(ts)
          if (!r) { r = { t: ts, disks: {} }; map.set(ts, r) }
          (r.disks[d] ||= {})[rw === 'r' ? 'readBps' : 'writeBps'] = v
        }
      }
      rows.value = [...map.values()].sort((a, b) => a.t - b.t)
    }
  } catch (e) { /* 忽略 */ }
}

onMounted(() => {
  reload()
  stop = poll(async () => {
    try {
      const sm = await getJSON('api/realtime')
      latest.value = sm
      if (winSec.value <= 900) {
        rows.value.push(sm)
        const cutoff = sm.t - winSec.value
        while (rows.value.length && rows.value[0].t < cutoff) rows.value.shift()
      }
    } catch (e) { /* 忽略 */ }
  }, 2000)
})
onBeforeUnmount(() => stop && stop())

const COLORS = ['#3fb950', '#d29922', '#39c5cf', '#bc8cff', '#2f81f7', '#f85149']
const ioSeries = computed(() => {
  const out = []
  const disks = [...new Set(rows.value.flatMap((r) => Object.keys(r.disks || {})))]
  disks.forEach((d, i) => {
    const pick = (f) => rows.value.filter((r) => r.disks && r.disks[d]).map((r) => [r.t, f(r.disks[d])])
    out.push({ name: `${d} ${t('misc.read')}`, color: COLORS[(i * 2) % COLORS.length], points: pick((v) => v.readBps || 0) })
    out.push({ name: `${d} ${t('misc.write')}`, color: COLORS[(i * 2 + 1) % COLORS.length], points: pick((v) => v.writeBps || 0) })
  })
  return out
})

const diskRows = computed(() => {
  const sm = latest.value
  if (!sm) return []
  return Object.entries(sm.disks || {}).map(([d, v]) => {
    const temp = (sm.temps || {})[d]
    const h = (sm.diskHealth || {})[d]
    return {
      dev: d,
      io: fmtBps((v.readBps || 0) + (v.writeBps || 0)),
      read: fmtBps(v.readBps || 0),
      write: fmtBps(v.writeBps || 0),
      temp,
      ioErr: h ? h.ioErr : null,
      state: h ? h.state : '',
    }
  })
})

const mountRows = computed(() => {
  const sm = latest.value
  if (!sm) return []
  return (sm.mounts || []).map((m) => ({
    dir: m.dir,
    usedPct: m.total ? ((m.total - m.free) / m.total) * 100 : 0,
    free: fmtBytes(m.free),
    total: fmtBytes(m.total),
  }))
})
</script>

<template>
  <div>
    <div class="ctrl">
      <span class="ctrl-label">{{ t('ctrl.window') }}</span>
      <button v-for="w in WINDOWS" :key="w" :class="['rbtn', { active: winSec === w }]"
        @click="winSec = w; reload()">{{ fmtDur(w, locale) }}</button>
    </div>

    <section class="panel">
      <LiveChart :series="ioSeries" y-type="bytes" height="34vh" />
    </section>

    <section class="panel">
      <h3>{{ t('disk.health') }}</h3>
      <table :style="{ minWidth: diskMinW + 'px' }">
        <colgroup>
          <col v-for="c in HCOLS.slice(0, -1)" :key="c.key" :style="{ width: (hcw[c.key] || c.w) + 'px' }" />
          <col />
        </colgroup>
        <thead><tr>
          <th v-for="c in HCOLS" :key="c.key" @mousemove="hcCursor(c.key, $event)" @pointerdown="hcR(c.key, $event)">
            {{ { dev: t('disk.dev'), io: t('disk.ioNow'), r: 'R', w: 'W', temp: t('disk.temp'), ioerr: 'IO err', state: t('disk.state') }[c.key] }}<span class="colgrip"></span>
          </th>
        </tr></thead>
        <tbody>
          <tr v-for="r in diskRows" :key="r.dev">
            <td class="mono">{{ r.dev }}</td>
            <td>{{ r.io }}</td><td>{{ r.read }}</td><td>{{ r.write }}</td>
            <td :style="{ color: r.temp ? pressureColor(((r.temp - 35) / 25) * 100) : undefined }">
              {{ r.temp != null ? fmtC(r.temp) : '-' }}</td>
            <td :class="{ bad: r.ioErr > 0 }">{{ r.ioErr != null ? r.ioErr : '-' }}</td>
            <td>{{ r.state || '-' }}</td>
          </tr>
        </tbody>
      </table>
      <p class="hint">{{ t('disk.smartNote') }}</p>
    </section>

    <section class="panel">
      <h3>{{ t('disk.fs') }}</h3>
      <div v-for="m in mountRows" :key="m.dir" class="fsrow">
        <span class="mono dir">{{ m.dir }}</span>
        <div class="fsbar">
          <div class="fsfill" :style="{ width: m.usedPct + '%', background: pressureColor(m.usedPct) }"></div>
        </div>
        <span class="fslabel">{{ m.usedPct.toFixed(1) }}% · {{ t('disk.free') }} {{ m.free }} / {{ m.total }}</span>
      </div>
    </section>
  </div>
</template>

<style scoped>
.ctrl { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-top: 14px; padding: 8px 12px; background: var(--panel); border: 1px solid var(--border); border-radius: 10px; }
.ctrl-label { color: var(--muted); font-size: 12px; }
.rbtn { background: none; border: 1px solid transparent; color: var(--muted); border-radius: 6px; padding: 3px 10px; cursor: pointer; font-size: 12px; }
.rbtn.active { color: var(--text); background: var(--bg); border-color: var(--border); }
.panel { margin-top: 12px; background: var(--panel); border: 1px solid var(--border); border-radius: 10px; padding: 10px 12px; overflow-x: auto; }
.panel h3 { margin: 2px 0 8px; font-size: 13px; color: var(--muted); font-weight: 500; }
table { border-collapse: collapse; font-size: 13px; table-layout: fixed; width: 100%; }
th { text-align: left; color: var(--muted); font-weight: 500; padding: 4px 10px 4px 0; white-space: nowrap; position: relative; }
.colgrip { position: absolute; right: 0; top: 0; bottom: 0; width: 10px; cursor: col-resize; }
th:hover .colgrip { background: rgba(47, 129, 247, 0.35); border-radius: 2px; }
td { padding: 6px 10px 6px 0; border-top: 1px solid var(--border); white-space: nowrap; }
.mono { font-family: ui-monospace, Consolas, monospace; font-size: 12px; }
.bad { color: var(--red); font-weight: 600; }
.hint { color: var(--muted); font-size: 12px; margin: 8px 0 0; }
.fsrow { display: flex; align-items: center; gap: 12px; margin: 8px 0; }
.dir { width: 90px; color: var(--muted); font-size: 12px; }
.fsbar { flex: 1; height: 8px; background: var(--border); border-radius: 4px; overflow: hidden; }
.fsfill { height: 100%; border-radius: 4px; transition: width .4s; }
.fslabel { font-size: 12px; color: var(--muted); white-space: nowrap; }
</style>
