<script setup>
import { onMounted, onBeforeUnmount, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { getJSON, postJSON, poll } from '../api'

const { t } = useI18n()
const data = ref(null)
const rules = ref([])
let stop = null

async function reload() {
  try {
    data.value = await getJSON('api/alerts')
    rules.value = (data.value.rules || []).map((r) => ({ ...r }))
  } catch (e) { /* 忽略 */ }
}

onMounted(() => {
  reload()
  stop = poll(reload, 30000)
})
onBeforeUnmount(() => stop && stop())

async function ack(id) {
  try { await postJSON('api/alerts/ack', { id }); reload() } catch (e) { /* 忽略 */ }
}

async function saveRules() {
  try {
    await postJSON('api/alerts/rules', { rules: rules.value })
    saved.value = true
    setTimeout(() => (saved.value = false), 1500)
    reload()
  } catch (e) { /* 忽略 */ }
}
const saved = ref(false)

const METRIC_KEYS = ['cpu', 'mem_pct', 'swap_pct', 'load1', 'fan', 'temp.any', 'temp.disk', 'fs']
function fmtTime(ts) {
  if (!ts) return '-'
  const d = new Date(ts * 1000)
  const p = (x) => String(x).padStart(2, '0')
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}
</script>

<template>
  <div v-if="data" class="alerts">
    <section class="panel">
      <h3>{{ t('alert.active') }} <span v-if="data.unread" class="badge-red">{{ data.unread }}</span></h3>
      <p v-if="!(data.active || []).length" class="hint">{{ t('alert.none') }}</p>
      <div v-for="a in data.active" :key="a.id" class="arow" :class="a.level">
        <span class="lvl">{{ a.level === 'crit' ? '🔴' : '🟡' }}</span>
        <span class="mono">{{ fmtTime(a.started) }}</span>
        <b>{{ t(`alert.rule.${a.ruleId}`, a.ruleId) }}</b>
        <span class="mono">{{ a.metric }} = {{ a.value.toFixed(1) }}</span>
        <button v-if="!a.acked" class="ack" @click="ack(a.id)">{{ t('alert.ack') }}</button>
        <span v-else class="acked">{{ t('alert.acked') }}</span>
      </div>
    </section>

    <section class="panel">
      <h3>{{ t('alert.rules') }} <button class="ack" @click="saveRules">{{ t('alert.save') }}</button>
        <span v-if="saved" class="ok">✓</span></h3>
      <table>
        <thead><tr>
          <th>{{ t('alert.enabled') }}</th><th>{{ t('alert.metric') }}</th><th>{{ t('alert.cond') }}</th>
          <th>{{ t('alert.forMin') }}</th><th>{{ t('alert.level') }}</th>
        </tr></thead>
        <tbody>
          <tr v-for="r in rules" :key="r.id">
            <td><input v-model="r.enabled" type="checkbox" /></td>
            <td><select v-model="r.metric" class="sel">
              <option v-for="m in METRIC_KEYS" :key="m" :value="m">{{ m }}</option>
            </select></td>
            <td><select v-model="r.op" class="sel"><option>&gt;</option><option>&lt;</option></select>
              <input v-model.number="r.threshold" type="number" step="any" class="num" /></td>
            <td><input v-model.number="r.forMin" type="number" min="0" max="720" class="num" /> min</td>
            <td><select v-model="r.level" class="sel"><option value="warn">warn</option><option value="crit">crit</option></select></td>
          </tr>
        </tbody>
      </table>
    </section>

    <section class="panel">
      <h3>{{ t('alert.history') }}</h3>
      <table>
        <thead><tr><th>{{ t('alert.started') }}</th><th>{{ t('alert.ended') }}</th><th>{{ t('alert.metric') }}</th><th>{{ t('alert.value') }}</th><th>{{ t('alert.level') }}</th></tr></thead>
        <tbody>
          <tr v-for="a in data.recent || []" :key="a.id">
            <td class="mono">{{ fmtTime(a.started) }}</td>
            <td class="mono">{{ a.resolved ? fmtTime(a.resolved) : t('alert.ongoing') }}</td>
            <td>{{ t(`alert.rule.${a.ruleId}`, a.ruleId) }}</td>
            <td class="mono">{{ a.value.toFixed(1) }}</td>
            <td :class="a.level">{{ a.level }}</td>
          </tr>
        </tbody>
      </table>
    </section>
  </div>
</template>

<style scoped>
.panel { margin-top: 12px; background: var(--panel); border: 1px solid var(--border); border-radius: 10px; padding: 10px 12px; }
.panel h3 { margin: 2px 0 8px; font-size: 13px; color: var(--muted); font-weight: 500; display: flex; align-items: center; gap: 10px; }
.badge-red { background: var(--red); color: #fff; border-radius: 10px; padding: 0 8px; font-size: 12px; }
.arow { display: flex; align-items: center; gap: 12px; padding: 7px 4px; border-top: 1px solid var(--border); font-size: 13px; flex-wrap: wrap; }
.arow.crit { background: rgba(248, 81, 73, 0.07); }
.ack { background: var(--bg); color: var(--text); border: 1px solid var(--border); border-radius: 6px; padding: 2px 10px; font-size: 12px; cursor: pointer; }
.acked { color: var(--muted); font-size: 12px; }
.ok { color: var(--green); }
.hint { color: var(--muted); font-size: 13px; }
table { width: 100%; border-collapse: collapse; font-size: 13px; }
th { text-align: left; color: var(--muted); font-weight: 500; padding: 4px 8px 4px 0; }
td { padding: 5px 8px 5px 0; border-top: 1px solid var(--border); }
td.crit, .crit { color: var(--red); }
.mono { font-family: ui-monospace, Consolas, monospace; font-size: 12px; }
.sel { background: var(--bg); color: var(--text); border: 1px solid var(--border); border-radius: 6px; padding: 2px 6px; font-size: 12px; }
.num { background: var(--bg); color: var(--text); border: 1px solid var(--border); border-radius: 6px; padding: 2px 6px; width: 76px; font-size: 12px; }
</style>
