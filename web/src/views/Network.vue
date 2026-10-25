<script setup>
import { computed, onMounted, onBeforeUnmount, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import LiveChart from '../components/LiveChart.vue'
import { getJSON, poll } from '../api'
import { fmtBps, fmtDur } from '../fmt'
import { useColResize } from '../colresize'

const { t, locale } = useI18n()
const NCOLS = [{ key: 'iface', w: 150 }, { key: 'rx', w: 110 }, { key: 'tx', w: 110 }, { key: 'IPv4', w: 140 }, { key: 'IPv6', w: 420 }]
const { widths: ncw, thResize: ncR, thCursor: ncCursor } = useColResize('netIf', Object.fromEntries(NCOLS.map((c) => [c.key, c.w])))
const latest = ref(null)
const meta = ref(null)
const rows = ref([])
let stop = null
let stopMeta = null

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
      for (const ifc of Object.keys(latest.value.net || {})) ms.push(`net.${ifc}.rx`, `net.${ifc}.tx`)
      const res = await getJSON(`api/series?metrics=${ms.slice(0, 24).join(',')}&start=${end - winSec.value}&end=${end}&level=l0`)
      const map = new Map()
      for (const [name, pts] of Object.entries(res.series || {})) {
        const [, ifc, dir] = name.split('.')
        for (const [ts, v] of pts) {
          let r = map.get(ts)
          if (!r) { r = { t: ts, net: {} }; map.set(ts, r) }
          (r.net[ifc] ||= {})[dir === 'rx' ? 'rxRate' : 'txRate'] = v
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
  stopMeta = poll(async () => { meta.value = await getJSON('api/meta') }, 600000)
})
onBeforeUnmount(() => { stop && stop(); stopMeta && stopMeta() })

const COLORS = ['#2f81f7', '#3fb950', '#d29922', '#bc8cff', '#39c5cf', '#f85149', '#7ee787', '#ff7b72']
const series = computed(() => {
  const out = []
  const ifaces = [...new Set(rows.value.flatMap((r) => Object.keys(r.net || {})))]
    .filter((i) => i !== 'lo' && !i.startsWith('veth'))
  ifaces.forEach((ifc, i) => {
    const pick = (f) => rows.value.filter((r) => r.net && r.net[ifc]).map((r) => [r.t, f(r.net[ifc])])
    const rx = pick((v) => v.rxRate || 0)
    const tx = pick((v) => v.txRate || 0)
    if (rx.some((p) => p[1] > 0) || tx.some((p) => p[1] > 0) || latest.value?.net?.[ifc]) {
      out.push({ name: `↓ ${ifc}`, color: COLORS[(i * 2) % COLORS.length], points: rx })
      out.push({ name: `↑ ${ifc}`, color: COLORS[(i * 2 + 1) % COLORS.length], points: tx })
    }
  })
  return out
})

const ifaceRows = computed(() => {
  const sm = latest.value
  if (!sm) return []
  return Object.entries(sm.net || {}).map(([ifc, v]) => ({
    ifc,
    rx: fmtBps(v.rxRate || 0),
    tx: fmtBps(v.txRate || 0),
    ip: (meta.value?.lanIPs || []).find((e) => e.iface === ifc),
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
      <LiveChart :series="series" y-type="bytes" height="42vh" />
    </section>
    <section class="panel">
      <h3>{{ t('net.ifaces') }}</h3>
      <table>
        <thead><tr><th>{{ t('net.iface') }}</th><th>↓</th><th>↑</th><th>IPv4</th><th>IPv6</th></tr></thead>
        <tbody>
          <tr v-for="r in ifaceRows" :key="r.ifc">
            <td class="mono">{{ r.ifc }}</td>
            <td>{{ r.rx }}</td><td>{{ r.tx }}</td>
            <td class="mono">{{ r.ip?.v4 || '-' }}</td>
            <td class="mono wrap">{{ r.ip?.v6 || '-' }}</td>
          </tr>
        </tbody>
      </table>
      <p v-if="meta" class="ips">
        {{ t('net.public') }}：IPv4 <span class="mono">{{ meta.publicIPv4 || '-' }}</span>
        ｜ IPv6 <span class="mono wrap">{{ meta.publicIPv6 || '-' }}</span>
      </p>
    </section>
  </div>
</template>

<style scoped>
.ctrl { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-top: 14px; padding: 8px 12px; background: var(--panel); border: 1px solid var(--border); border-radius: 10px; }
.ctrl-label { color: var(--muted); font-size: 12px; }
.rbtn { background: none; border: 1px solid transparent; color: var(--muted); border-radius: 6px; padding: 3px 10px; cursor: pointer; font-size: 12px; }
.rbtn.active { color: var(--text); background: var(--bg); border-color: var(--border); }
.panel { margin-top: 12px; background: var(--panel); border: 1px solid var(--border); border-radius: 10px; padding: 10px 12px; }
.panel h3 { margin: 2px 0 8px; font-size: 13px; color: var(--muted); font-weight: 500; }
table { border-collapse: collapse; font-size: 13px; table-layout: fixed; width: 100%; }
th { text-align: left; color: var(--muted); font-weight: 500; padding: 4px 10px 4px 0; position: relative; white-space: nowrap; }
.colgrip { position: absolute; right: 0; top: 0; bottom: 0; width: 10px; cursor: col-resize; }
th:hover .colgrip { background: rgba(47, 129, 247, 0.35); border-radius: 2px; }
td { padding: 5px 10px 5px 0; border-top: 1px solid var(--border); vertical-align: top; }
.mono { font-family: ui-monospace, Consolas, monospace; font-size: 12px; }
.wrap { white-space: normal; word-break: break-all; max-width: 420px; }
.ips { color: var(--muted); font-size: 12px; }
</style>
