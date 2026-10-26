<script setup>
import { computed, onMounted, onBeforeUnmount, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { getJSON, poll } from '../api'
import { fmtBytes, fmtUptime } from '../fmt'
import { useColResize } from '../colresize'

const { t, locale } = useI18n()
const procs = ref([])
const q = ref('')
const sortKey = ref('cpu')
const sortDesc = ref(true)
let stop = null

// 进程页独立刷新间隔（差分 CPU% 会随间隔自适应）
const REFRESH = [1, 2, 5, 10]
const refreshSec = ref(2)
let stopPoll = null

async function startPolling() {
  if (stopPoll) stopPoll()
  stopPoll = poll(async () => {
    try { procs.value = (await getJSON('api/processes')).procs } catch (e) { /* 忽略 */ }
  }, refreshSec.value * 1000)
}

onMounted(startPolling)
onBeforeUnmount(() => stopPoll && stopPoll())
function setRefresh(v) { refreshSec.value = v; startPolling() }

// Linux 进程状态码翻译（未知码原样显示）
function stateText(s) {
  const translated = t(`proc.st.${s}`)
  return translated === `proc.st.${s}` ? s : `${s} ${translated}`
}

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

const COLS = [
  { key: 'pid', w: 80 },
  { key: 'cpu', w: 90 },
  { key: 'rss', w: 100 },
  { key: 'vmSwap', w: 100 },
  { key: 'state', w: 120 },
  { key: 'startAt', w: 110 },
  { key: 'cmd', w: 520 },
]
const { widths, thResize, thCursor } = useColResize('procs', Object.fromEntries(COLS.map((c) => [c.key, c.w])))
// 窄屏不压缩列：最小总宽（末列保底 260px），容器横向滚动
const procMinW = COLS.reduce((a, c) => a + c.w, 0) - COLS[COLS.length - 1].w + 260

function setSort(k) {
  if (sortKey.value === k) sortDesc.value = !sortDesc.value
  else { sortKey.value = k; sortDesc.value = true }
}
function now() { return Date.now() / 1000 }
const zh = computed(() => locale.value)
</script>

<template>
  <div class="procs">
    <div class="ctrl">
      <input v-model="q" class="search" :placeholder="t('proc.search')" />
      <span class="ctrl-label">{{ t('proc.count', { n: procs.length }) }}</span>
      <span class="ctrl-label">{{ t('proc.sumCpu', { n: totalCpu.toFixed(0) }) }}</span>
      <span class="ctrl-label">{{ t('proc.cpuNote') }}</span>
      <span class="spacer"></span>
      <span class="ctrl-label">{{ t('ctrl.sampleInterval') }}</span>
      <select class="sel" :value="refreshSec" @change="setRefresh(parseFloat($event.target.value))">
        <option v-for="v in REFRESH" :key="v" :value="v">{{ v }}s</option>
      </select>
    </div>
    <section class="panel">
      <table :style="{ minWidth: procMinW + 'px' }">
        <colgroup>
          <col v-for="c in COLS.slice(0, -1)" :key="c.key" :style="{ width: (widths[c.key] || c.w) + 'px' }" />
          <col /><!-- 末列自适应剩余空间 -->
        </colgroup>
        <thead><tr>
          <th v-for="c in COLS" :key="c.key" :class="{ sortable: c.key !== 'cmd' }"
            @mousemove="thCursor(c.key, $event)"
            @pointerdown="thResize(c.key, $event)" @click="c.key !== 'cmd' && setSort(c.key)">
            {{ t(`proc.col.${c.key}`) }}
            <span v-if="sortKey === c.key" class="arrow">{{ sortDesc ? '▼' : '▲' }}</span>
            <span class="colgrip"></span>
          </th>
        </tr></thead>
        <tbody>
          <tr v-for="p in filtered.slice(0, 400)" :key="p.pid">
            <td class="mono">{{ p.pid }}</td>
            <td class="mono" :style="{ color: p.cpu > 800 ? 'var(--red)' : p.cpu > 100 ? 'var(--orange)' : undefined }">
              {{ p.cpu.toFixed(1) }}%</td>
            <td class="mono">{{ fmtBytes(p.rss) }}</td>
            <td class="mono">{{ p.vmSwap ? fmtBytes(p.vmSwap) : '-' }}</td>
            <td>{{ stateText(p.state) }}</td>
            <td class="mono">{{ p.startAt ? fmtUptime(Math.max(0, now() - p.startAt), zh) : '-' }}</td>
            <td class="cmd" :title="p.cmd">{{ p.cmd }}</td>
          </tr>
        </tbody>
      </table>
      <p v-if="filtered.length > 400" class="hint">{{ t('proc.truncated', { n: filtered.length }) }}</p>
      <p class="hint">{{ t('proc.limitNote') }}</p>
    </section>
  </div>
</template>

<style scoped>
.ctrl { display: flex; align-items: center; gap: 16px; flex-wrap: wrap; margin-top: 14px; padding: 8px 12px; background: var(--panel); border: 1px solid var(--border); border-radius: 10px; }
.ctrl-label { color: var(--muted); font-size: 12px; }
.spacer { flex: 1; }
.search { background: var(--bg); color: var(--text); border: 1px solid var(--border); border-radius: 6px; padding: 5px 10px; font-size: 13px; width: 240px; }
.sel { background: var(--bg); color: var(--text); border: 1px solid var(--border); border-radius: 6px; padding: 3px 8px; font-size: 12px; cursor: pointer; }
.panel { margin-top: 12px; background: var(--panel); border: 1px solid var(--border); border-radius: 10px; padding: 10px 12px; overflow-x: auto; }
table { border-collapse: collapse; font-size: 13px; table-layout: fixed; width: 100%; }
th { text-align: left; color: var(--muted); font-weight: 500; padding: 4px 10px 4px 0; white-space: nowrap; position: relative; user-select: none; }
th.sortable { cursor: pointer; }
th.sortable:hover { color: var(--text); }
.arrow { color: var(--accent); font-size: 10px; margin-left: 2px; }
.colgrip { position: absolute; right: 0; top: 0; bottom: 0; width: 10px; cursor: col-resize; }
th:hover .colgrip { background: rgba(47, 129, 247, 0.35); border-radius: 2px; }
td { padding: 4px 10px 4px 0; border-top: 1px solid var(--border); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.cmd { overflow: hidden; text-overflow: ellipsis; }
.mono { font-family: ui-monospace, Consolas, monospace; font-size: 12px; }
.hint { color: var(--muted); font-size: 12px; }
</style>
