<script setup>
import { computed, onMounted, onBeforeUnmount, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import LiveChart from '../components/LiveChart.vue'
import StatCard from '../components/StatCard.vue'
import { getJSON, poll } from '../api'
import { fmtC, fmtDur, pressureColor, tempName, tempTip } from '../fmt'

const { t, locale } = useI18n()
const latest = ref(null)
const rows = ref([])
let stop = null

const WINDOWS = [900, 3600, 21600, 86400]
const winSec = ref(900)

async function reload() {
  try {
    latest.value = await getJSON('api/realtime')
    if (winSec.value <= 900) {
      const ring = await getJSON(`api/ring?sec=${winSec.value}`)
      rows.value = ring
    } else {
      const end = Math.floor(Date.now() / 1000)
      const keys = Object.keys(latest.value.temps || {}).map((k) => 'temp.' + k)
      const res = await getJSON(`api/series?metrics=${keys.join(',')}&start=${end - winSec.value}&end=${end}&level=l0`)
      const map = new Map()
      for (const [name, pts] of Object.entries(res.series || {})) {
        const k = name.slice(5)
        for (const [ts, v] of pts) {
          let r = map.get(ts)
          if (!r) { r = { t: ts, temps: {} }; map.set(ts, r) }
          r.temps[k] = v
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

const COLORS = ['#f85149', '#d29922', '#bc8cff', '#39c5cf', '#2f81f7', '#3fb950', '#ff7b72', '#7ee787']
const series = computed(() => {
  const keys = [...new Set(rows.value.flatMap((r) => Object.keys(r.temps || {})))]
  return keys.map((k, i) => ({
    name: tempName(k, locale.value),
    color: COLORS[i % COLORS.length],
    points: rows.value.filter((r) => r.temps && r.temps[k] != null).map((r) => [r.t, r.temps[k]]),
  }))
})

const cards = computed(() => {
  const sm = latest.value
  if (!sm) return []
  return Object.entries(sm.temps || {})
    .sort((a, b) => b[1] - a[1])
    .slice(0, 9)
    .map(([k, v]) => ({
      title: tempName(k, locale.value),
      value: fmtC(v),
      color: pressureColor(((v - 35) / 50) * 100),
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
    <div class="cards">
      <StatCard v-for="(c, i) in cards" :key="i" v-bind="c" />
    </div>
    <section class="panel">
      <LiveChart :series="series" y-type="celsius" height="48vh" />
      <p class="hint">{{ tempTip(locale.value) }}</p>
    </section>
  </div>
</template>

<style scoped>
.ctrl { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-top: 14px; padding: 8px 12px; background: var(--panel); border: 1px solid var(--border); border-radius: 10px; }
.ctrl-label { color: var(--muted); font-size: 12px; }
.rbtn { background: none; border: 1px solid transparent; color: var(--muted); border-radius: 6px; padding: 3px 10px; cursor: pointer; font-size: 12px; }
.rbtn.active { color: var(--text); background: var(--bg); border-color: var(--border); }
.hint { color: var(--muted); font-size: 12px; margin: 8px 0 0; }
.cards { display: grid; gap: 10px; grid-template-columns: repeat(auto-fill, minmax(170px, 1fr)); margin-top: 12px; }
.panel { margin-top: 12px; background: var(--panel); border: 1px solid var(--border); border-radius: 10px; padding: 10px 12px 6px; }
</style>
