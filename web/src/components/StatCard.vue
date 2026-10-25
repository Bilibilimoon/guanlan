<script setup>
defineProps({
  title: String,
  value: String,
  sub: String,
  subs: Array,      // 可选多行小字（如 IP 列表）
  bar: Number,      // 0-100 可选进度条
  color: { type: String, default: 'var(--accent)' },
})
</script>

<template>
  <div class="card">
    <div class="card-title">{{ title }}</div>
    <div class="card-value" :style="{ color }">{{ value }}</div>
    <div v-if="sub" class="card-sub">{{ sub }}</div>
    <div v-for="(line, i) in subs || []" :key="i" class="card-sub mono" :title="line">{{ line }}</div>
    <div v-if="bar != null" class="card-bar">
      <div class="card-bar-fill" :style="{ width: Math.min(bar, 100) + '%', background: color }"></div>
    </div>
  </div>
</template>

<style scoped>
.card {
  background: var(--panel); border: 1px solid var(--border);
  border-radius: 10px; padding: 12px 14px; min-width: 0;
}
.card-title { color: var(--muted); font-size: 12px; margin-bottom: 6px; white-space: nowrap; }
.card-value { font-size: 22px; font-weight: 600; font-variant-numeric: tabular-nums; }
.card-sub { color: var(--muted); font-size: 12px; margin-top: 4px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.mono {
  font-family: ui-monospace, 'SF Mono', Consolas, monospace; font-size: 11px;
  white-space: normal; word-break: break-all; line-height: 1.5;
}
.card-bar { height: 4px; background: var(--border); border-radius: 2px; margin-top: 8px; overflow: hidden; }
.card-bar-fill { height: 100%; border-radius: 2px; transition: width 0.4s; }
</style>
