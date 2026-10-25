<script setup>
import { computed, onMounted, onBeforeUnmount, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { getJSON, poll } from '../api'
import { fmtBytes, fmtUptime } from '../fmt'

const { t } = useI18n()
const procs = ref([])
const q = ref('')
const sortKey = ref('cpu')
const sortDesc = ref(true)
let stop = null

onMounted(() => {
  stop = poll(async () => {
    try { procs.value = (await getJSON('api/processes')).procs } catch (e) { /* 忽略 */ }
  }, 2000)
})
onBeforeUnmount(() => stop && stop())

const filtered = computed(() => {
  const kw = q.value.trim().toLowerCase()
  let list = procs.value
  if (kw) {
    list = list.filter((p) =>
      p.comm.toLowerCase().includes(kw) || p.cmd.toLowerCase().includes(kw) || String(p.pid) === kw)
  }
  const key = sortKey.value
  return [...list].sort((a, b) => (sortDesc.value ? b[key] - a[key] : a[key] - b[key]))
})

const totalCpu = computed(() => procs.value.reduce((s, p) => s + p.cpu, 0))

function setSort(k) {
  if (sortKey.value === k) sortDesc.value = !sortDesc.value
  else { sortKey.value = k; sortDesc.value = true }
}
function now() { return Date.now() / 1000 }
</script>

<template>
  <div class="procs">
    <div class="ctrl">
      <input v-model="q" class="search" :placeholder="t('proc.search')" />
      <span class="ctrl-label">{{ t('proc.count', { n: procs.length }) }}</span>
      <span class="ctrl-label">{{ t('proc.sumCpu', { n: totalCpu.toFixed(0) }) }}</span>
      <span class="ctrl-label">{{ t('proc.cpuNote') }}</span>
    </div>
    <section class="panel">
      <table>
        <thead><tr>
          <th class="sortable" @click="setSort('pid')">PID</th>
          <th class="sortable" @click="setSort('cpu')">{{ t('proc.cpu') }}</th>
          <th class="sortable" @click="setSort('rss')">{{ t('proc.mem') }}</th>
          <th>{{ t('proc.name') }}</th>
          <th>{{ t('proc.state') }}</th>
          <th class="sortable" @click="setSort('startAt')">{{ t('proc.uptime') }}</th>
        </tr></thead>
        <tbody>
          <tr v-for="p in filtered.slice(0, 400)" :key="p.pid">
            <td class="mono">{{ p.pid }}</td>
            <td class="mono" :style="{ color: p.cpu > 800 ? 'var(--red)' : p.cpu > 100 ? 'var(--orange)' : undefined }">
              {{ p.cpu.toFixed(1) }}%</td>
            <td class="mono">{{ fmtBytes(p.rss) }}</td>
            <td class="cmd" :title="p.cmd">{{ p.cmd }}</td>
            <td>{{ p.state }}</td>
            <td class="mono">{{ p.startAt ? fmtUptime(Math.max(0, now() - p.startAt)) : '-' }}</td>
          </tr>
        </tbody>
      </table>
      <p v-if="filtered.length > 400" class="hint">{{ t('proc.truncated', { n: filtered.length }) }}</p>
    </section>
  </div>
</template>

<style scoped>
.ctrl { display: flex; align-items: center; gap: 16px; flex-wrap: wrap; margin-top: 14px; padding: 8px 12px; background: var(--panel); border: 1px solid var(--border); border-radius: 10px; }
.ctrl-label { color: var(--muted); font-size: 12px; }
.search { background: var(--bg); color: var(--text); border: 1px solid var(--border); border-radius: 6px; padding: 5px 10px; font-size: 13px; width: 240px; }
.panel { margin-top: 12px; background: var(--panel); border: 1px solid var(--border); border-radius: 10px; padding: 10px 12px; }
table { width: 100%; border-collapse: collapse; font-size: 13px; }
th { text-align: left; color: var(--muted); font-weight: 500; padding: 4px 10px 4px 0; white-space: nowrap; }
th.sortable { cursor: pointer; user-select: none; }
th.sortable:hover { color: var(--text); }
td { padding: 4px 10px 4px 0; border-top: 1px solid var(--border); white-space: nowrap; }
.cmd { max-width: 480px; overflow: hidden; text-overflow: ellipsis; }
.mono { font-family: ui-monospace, Consolas, monospace; font-size: 12px; }
.hint { color: var(--muted); font-size: 12px; }
</style>
