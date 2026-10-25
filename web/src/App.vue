<script setup>
import { computed, defineAsyncComponent, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Overview from './views/Overview.vue'
import { getJSON, onAdminDenied, poll } from './api'

const Processes = defineAsyncComponent(() => import('./views/Processes.vue'))
const Thermal = defineAsyncComponent(() => import('./views/Thermal.vue'))
const Network = defineAsyncComponent(() => import('./views/Network.vue'))
const Disks = defineAsyncComponent(() => import('./views/Disks.vue'))
const History = defineAsyncComponent(() => import('./views/History.vue'))
const Alerts = defineAsyncComponent(() => import('./views/Alerts.vue'))
const Settings = defineAsyncComponent(() => import('./views/Settings.vue'))

const { t } = useI18n()
const tab = ref('overview')
const tabs = ['overview', 'processes', 'thermal', 'network', 'disks', 'history', 'alerts', 'settings']
const denied = ref(false)
const unreadAlerts = ref(0) // 有未确认告警时「异常警告」tab 标红
onAdminDenied(() => { denied.value = true })

let stopAlertPoll = null
onMounted(() => {
  stopAlertPoll = poll(async () => {
    try {
      const d = await getJSON('api/alerts')
      unreadAlerts.value = d.unread || 0
    } catch (e) { /* 忽略 */ }
  }, 30000)
})
onBeforeUnmount(() => stopAlertPoll && stopAlertPoll())
</script>

<template>
  <div class="app">
    <header class="topbar">
      <div class="brand">
        <span class="logo">🌊</span>
        <span class="title">{{ t('appTitle') }}</span>
      </div>
      <nav class="tabs">
        <button v-for="tb in tabs" :key="tb"
          :class="['tab', { active: tab === tb, alerting: tb === 'alerts' && unreadAlerts > 0 }]"
          @click="tab = tb">
          {{ t(`tabs.${tb}`) }}
          <span v-if="tb === 'alerts' && unreadAlerts > 0" class="badge">●</span>
        </button>
      </nav>
    </header>

    <div v-if="denied" class="denied">{{ t('adminOnly') }}</div>
    <main v-else class="main">
      <Overview v-if="tab === 'overview'" />
      <Processes v-else-if="tab === 'processes'" />
      <Thermal v-else-if="tab === 'thermal'" />
      <Network v-else-if="tab === 'network'" />
      <Disks v-else-if="tab === 'disks'" />
      <History v-else-if="tab === 'history'" />
      <Alerts v-else-if="tab === 'alerts'" />
      <Settings v-else-if="tab === 'settings'" />
    </main>
  </div>
</template>

<style>
:root {
  --bg: #0d1117;
  --panel: #161b22;
  --border: #21262d;
  --text: #e6edf3;
  --muted: #8b949e;
  --accent: #2f81f7;
  --green: #3fb950;
  --orange: #d29922;
  --red: #f85149;
}
* { box-sizing: border-box; }
html, body { margin: 0; padding: 0; background: var(--bg); color: var(--text); }
body { font: 14px/1.5 -apple-system, 'Segoe UI', 'PingFang SC', 'Microsoft YaHei', sans-serif; }

.app { width: 100%; margin: 0 auto; padding: 0 clamp(12px, 2vw, 32px) 32px; }
.topbar {
  display: flex; align-items: center; gap: 16px;
  padding: 12px 0; border-bottom: 1px solid var(--border);
  flex-wrap: wrap;
}
.brand { display: flex; align-items: center; gap: 8px; }
.title { font-size: 17px; font-weight: 600; letter-spacing: 0.5px; }
.tabs { display: flex; gap: 4px; flex-wrap: wrap; }
.tab {
  background: none; border: none; color: var(--muted);
  padding: 6px 12px; border-radius: 6px; cursor: pointer; font-size: 14px;
}
.tab:hover { color: var(--text); background: var(--panel); }
.tab.active { color: var(--text); background: var(--panel); }
.tab.alerting { color: var(--red); font-weight: 600; }
.badge { font-size: 10px; }
.denied {
  margin-top: 24px; padding: 16px; text-align: center;
  background: rgba(248, 81, 73, 0.1); border: 1px solid var(--red); border-radius: 8px;
}
.wip {
  margin-top: 40px; text-align: center; color: var(--muted);
}
</style>
