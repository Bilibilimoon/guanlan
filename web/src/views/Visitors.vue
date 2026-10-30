<script setup>
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { getJSON, poll } from '../api'
import { useColResize } from '../colresize'
import { fmtFullMs } from '../fmt'

const { t } = useI18n()
const data = ref(null)
const hours = ref(24)
const ipFilter = ref('')
const onlyFail = ref(false)
const expanded = ref('')
let stop = null

const RANGES = [1, 6, 24, 168, 720]

const VCOLS = [
  { key: 'state', w: 46 }, { key: 'ip', w: 250 }, { key: 'class', w: 90 },
  { key: 'page', w: 70 }, { key: 'n', w: 80 }, { key: 'fail', w: 70 },
  { key: 'first', w: 130 }, { key: 'last', w: 130 },
  { key: 'topics', w: 220 }, { key: 'ua', w: 240 },
]
const vMinW = VCOLS.reduce((a, c) => a + c.w, 0)
const { widths: vw, thResize: vResize, thCursor: vCursor } = useColResize(
  'visitors', Object.fromEntries(VCOLS.map((c) => [c.key, c.w])),
)

const items = computed(() => data.value?.items || [])

async function reload() {
  const p = new URLSearchParams()
  p.set('hours', String(hours.value))
  if (ipFilter.value.trim()) p.set('ip', ipFilter.value.trim())
  if (onlyFail.value) p.set('fail', '1')
  try { data.value = await getJSON('api/visitors?' + p.toString()) } catch (e) { /* 忽略 */ }
}

onMounted(() => {
  reload()
  stop = poll(reload, 30000)
})
onBeforeUnmount(() => stop && stop())

function setHours(v) {
  hours.value = parseInt(v, 10)
  reload()
}

function rangeKey(h) {
  return h === 1 ? 'h1' : h === 6 ? 'h6' : h === 24 ? 'h24' : h === 168 ? 'd7' : 'd30'
}

// 失败颜色：1~2 次橙色，3 次以上红色（用户定的规则）
function state(it) {
  const f = it.s4 + it.s5
  if (f >= 3) return 'crit'
  if (f >= 1) return 'warn'
  return 'ok'
}

function fmtTime(ts) {
  if (!ts) return '-'
  const d = new Date(ts * 1000)
  const p = (x) => String(x).padStart(2, '0')
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

function fmtFull(ts) {
  return ts ? fmtFullMs(ts * 1000) : '-'
}

function topicLabel(name) {
  if (name === 'root') return t('visitors.topic.root')
  if (name === 'assets') return t('visitors.topic.assets')
  if (name.startsWith('app:')) return name.slice(4)
  return name
}

function topicsText(it) {
  const out = (it.topics || []).map((tp) => `${topicLabel(tp.name)} ${tp.n}`).join(' · ')
  return out || '-'
}

function toggle(ip) {
  expanded.value = expanded.value === ip ? '' : ip
}
</script>

<template>
  <div v-if="data" class="visitors">
    <div class="ctrl">
      <span class="ctrl-label">{{ t('ctrl.window') }}</span>
      <select class="sel" :value="hours" @change="setHours($event.target.value)">
        <option v-for="h in RANGES" :key="h" :value="h">{{ t(`visitors.range.${rangeKey(h)}`) }}</option>
      </select>
      <input v-model="ipFilter" class="search" :placeholder="t('visitors.search')"
        @keyup.enter="reload" @change="reload" />
      <label class="chk"><input v-model="onlyFail" type="checkbox" @change="reload" /> {{ t('visitors.onlyFail') }}</label>
      <span class="spacer"></span>
      <span class="sum">{{ t('visitors.summary', { ips: data.count, req: data.totalReq, fail: data.totalFail }) }}</span>
    </div>

    <p class="src" :class="{ bad: !data.source.available }">
      <template v-if="data.source.available">
        {{ t('visitors.source.ok', { path: data.source.path, parsed: data.source.parsed, at: fmtTime(data.source.lastParse) }) }}
      </template>
      <template v-else>
        {{ t('visitors.source.bad', { error: data.source.error || t('visitors.source.missing') }) }}
      </template>
    </p>

    <section class="panel">
      <table :style="{ minWidth: vMinW + 'px' }">
        <colgroup>
          <col v-for="c in VCOLS.slice(0, -1)" :key="c.key" :style="{ width: (vw[c.key] || c.w) + 'px' }" />
          <col />
        </colgroup>
        <thead><tr>
          <th v-for="c in VCOLS" :key="c.key" @mousemove="vCursor(c.key, $event)" @pointerdown="vResize(c.key, $event)">
            {{ t(`visitors.h.${c.key}`) }}<span class="colgrip"></span>
          </th>
        </tr></thead>
        <tbody>
          <template v-for="it in items" :key="it.ip">
            <tr class="row clickable" :class="state(it)" @click="toggle(it.ip)">
              <td class="mono dot">{{ state(it) === 'ok' ? '·' : '●' }}</td>
              <td class="mono ip">{{ it.ip }}</td>
              <td>{{ t(`visitors.cls.${it.class}`) }}</td>
              <td class="mono">{{ it.page }}</td>
              <td class="mono">{{ it.n }}</td>
              <td class="mono" :class="state(it)">{{ it.s4 + it.s5 }}</td>
              <td class="mono">{{ fmtTime(it.first) }}</td>
              <td class="mono">{{ fmtTime(it.last) }}</td>
              <td class="topics">{{ topicsText(it) }}</td>
              <td class="ua" :title="it.ua">{{ it.ua || '-' }}</td>
            </tr>
            <tr v-if="expanded === it.ip" class="detail">
              <td :colspan="VCOLS.length">
                <div class="drow">
                  <span>{{ t('visitors.failDist', { s2: it.s2, s3: it.s3, s4: it.s4, s5: it.s5 }) }}</span>
                  <span class="mono">{{ t('visitors.firstAt') }} {{ fmtFull(it.first) }}</span>
                  <span class="mono">{{ t('visitors.lastAt') }} {{ fmtFull(it.last) }}</span>
                </div>
                <div class="drow wrap">
                  <b>{{ t('visitors.h.topics') }}：</b>
                  <span v-for="tp in it.topics" :key="tp.name" class="chip">{{ topicLabel(tp.name) }} × {{ tp.n }}</span>
                </div>
                <div class="drow wrap mono small">{{ it.ua || '-' }}</div>
              </td>
            </tr>
          </template>
        </tbody>
      </table>
      <p v-if="!items.length" class="hint">{{ t('visitors.none') }}</p>
    </section>
    <p class="hint">{{ t('visitors.hint') }}</p>
  </div>
</template>

<style scoped>
.ctrl { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-top: 14px; padding: 8px 12px; background: var(--panel); border: 1px solid var(--border); border-radius: 10px; }
.ctrl-label { color: var(--muted); font-size: 12px; }
.sel { background: var(--bg); color: var(--text); border: 1px solid var(--border); border-radius: 6px; padding: 3px 8px; font-size: 12px; cursor: pointer; }
.search { background: var(--bg); color: var(--text); border: 1px solid var(--border); border-radius: 6px; padding: 3px 8px; font-size: 12px; width: 200px; }
.chk { color: var(--muted); font-size: 12px; display: flex; align-items: center; gap: 4px; cursor: pointer; }
.spacer { flex: 1; }
.sum { color: var(--muted); font-size: 12px; }
.src { color: var(--muted); font-size: 12px; margin: 10px 4px 0; }
.src.bad { color: var(--red); }
.panel { margin-top: 8px; background: var(--panel); border: 1px solid var(--border); border-radius: 10px; padding: 10px 12px; overflow-x: auto; }
table { border-collapse: collapse; font-size: 13px; table-layout: fixed; width: 100%; }
th { text-align: left; color: var(--muted); font-weight: 500; padding: 4px 8px 4px 0; position: relative; white-space: nowrap; }
td { padding: 5px 8px 5px 0; border-top: 1px solid var(--border); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; vertical-align: top; }
.clickable { cursor: pointer; }
.row.warn { background: rgba(210, 153, 34, 0.06); }
.row.crit { background: rgba(248, 81, 73, 0.07); }
.row.warn .dot, .warn { color: var(--orange); }
.row.crit .dot, .crit { color: var(--red); }
.dot { color: var(--muted); }
.ip { font-size: 12px; }
.topics { font-size: 12px; }
.ua { color: var(--muted); font-size: 12px; }
.detail td { background: rgba(47, 129, 247, 0.06); padding: 8px 10px; white-space: normal; }
.drow { display: flex; gap: 16px; flex-wrap: wrap; font-size: 12px; color: var(--muted); padding: 2px 0; }
.drow.wrap { align-items: center; }
.chip { background: var(--bg); border: 1px solid var(--border); border-radius: 10px; padding: 1px 8px; font-size: 12px; }
.small { font-size: 11px; word-break: break-all; }
.mono { font-family: ui-monospace, Consolas, monospace; font-size: 12px; }
.hint { color: var(--muted); font-size: 12px; margin: 8px 4px 0; }
.colgrip { position: absolute; right: 0; top: 0; bottom: 0; width: 10px; cursor: col-resize; }
th:hover .colgrip { background: rgba(47, 129, 247, 0.35); border-radius: 2px; }
</style>
