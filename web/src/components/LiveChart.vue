<script setup>
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import * as echarts from 'echarts/core'
import { LineChart } from 'echarts/charts'
import { GridComponent, LegendComponent, TooltipComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import { fmtBytes, fmtClockMs, fmtFullMs, fmtAxisMs } from '../fmt'

echarts.use([LineChart, GridComponent, LegendComponent, TooltipComponent, CanvasRenderer])

const props = defineProps({
  // [{ name, color, points: [[unixSec, value], ...] }]（秒，进图前统一转毫秒）
  series: { type: Array, default: () => [] },
  // percent | celsius | bytes | rpm | count
  yType: { type: String, default: 'percent' },
  height: { type: String, default: '220px' },
  legend: { type: Boolean, default: true },
  // 启用滚轮缩放（历史页用）；变化时重置为全量视图
  zoom: { type: Boolean, default: false },
  resetKey: { type: Number, default: 0 },
})

const emit = defineEmits(['viewchange'])

const el = ref(null)
let chart = null
let ro = null
let lastPos = null // 悬停中的像素坐标（canvas 系）：数据刷新后据此恢复 tooltip

function fmtVal(v) {
  switch (props.yType) {
    case 'percent': return `${v.toFixed(1)}%`
    case 'celsius': return `${v.toFixed(1)}°C`
    case 'bytes': return fmtBytes(v) + '/s'
    case 'rpm': return `${Math.round(v)} RPM`
    default: return String(Math.round(v * 100) / 100)
  }
}

function yAxisOpt() {
  const base = { type: 'value', scale: false,
    axisLabel: { color: '#8b949e', hideOverlap: true },
    splitLine: { lineStyle: { color: '#21262d' } } }
  switch (props.yType) {
    case 'percent': return { ...base, max: 100, axisLabel: { ...base.axisLabel, formatter: '{value}%' } }
    case 'celsius': return { ...base, axisLabel: { ...base.axisLabel, formatter: '{value}°' } }
    case 'bytes': return { ...base, axisLabel: { ...base.axisLabel, formatter: (v) => fmtBytes(v) } }
    default: return base
  }
}

function render() {
  if (!chart) return
  // 数据跨度（秒域）：决定 X 轴格式
  let mn = Infinity, mx = -Infinity
  for (const s of props.series) {
    for (const p of s.points) {
      if (p[0] < mn) mn = p[0]
      if (p[0] > mx) mx = p[0]
    }
  }
  const spanSec = isFinite(mn) ? mx - mn : 0

  chart.setOption({
    animation: false,
    backgroundColor: 'transparent',
    tooltip: {
      trigger: 'axis',
      backgroundColor: '#161b22',
      borderColor: '#21262d',
      textStyle: { color: '#e6edf3', fontSize: 12 },
      formatter: (params) => {
        if (!params.length) return ''
        let html = `<b>${fmtFullMs(params[0].value[0])}</b>`
        for (const p of params) {
          html += `<br/>${p.marker} ${p.seriesName}　<b>${fmtVal(p.value[1])}</b>`
        }
        return html
      },
    },
    legend: props.legend
      ? {
          // 滚动式图例：条目再多也只占一行（翻页箭头），不会膨胀到与坐标轴重叠
          type: 'scroll',
          top: 0, right: 0,
          itemWidth: 14, itemHeight: 8,
          textStyle: { color: '#8b949e', fontSize: 11 },
          pageIconColor: '#8b949e',
          pageIconInactiveColor: '#2d333b',
          pageIconSize: 10,
          pageTextStyle: { color: '#8b949e', fontSize: 10 },
        }
      : undefined,
    grid: { left: 56, right: 14, top: props.legend ? 30 : 8, bottom: 24 },
    xAxis: {
      type: 'time',
      axisLabel: {
        color: '#8b949e',
        formatter: (v) => fmtAxisMs(v, spanSec),
        hideOverlap: true,
      },
      splitLine: { show: false },
    },
    yAxis: yAxisOpt(),
    dataZoom: props.zoom
      ? [{ type: 'inside', zoomOnMouseWheel: true, moveOnMouseMove: true, moveOnMouseWheel: true, filterMode: 'none' }]
      : undefined,
    series: props.series.map((s) => ({
      name: s.name,
      type: 'line',
      showSymbol: false,
      sampling: 'lttb',
      lineStyle: { width: 1.5 },
      itemStyle: { color: s.color },
      data: s.points.map(([t, v]) => [t * 1000, v]), // 秒 -> 毫秒（ECharts time 轴原生域）
    })),
    // replaceMerge：只替换 series，保留 tooltip / axisPointer 等组件状态——
    // 数据刷新不打断悬停中的 tooltip
  }, { replaceMerge: ['series'] })

  // 兜底：若 tooltip 仍被隐藏（如组件重建），按悬停像素坐标恢复
  if (lastPos) {
    const pos = lastPos
    requestAnimationFrame(() => {
      chart && chart.dispatchAction({ type: 'showTip', x: pos.x, y: pos.y })
    })
  }
}

function bindHover() {
  // zrender 层事件：坐标最可靠
  chart.getZr().on('mousemove', (e) => {
    lastPos = { x: e.zrX, y: e.zrY }
  })
  chart.getZr().on('globalout', () => { lastPos = null })
  if (props.zoom) {
    chart.on('dataZoom', () => {
      const dz = chart.getOption().dataZoom
      if (dz && dz[0] && emit) {
        emit('viewchange', { startValue: dz[0].startValue, endValue: dz[0].endValue })
      }
    })
  }
}

watch(() => props.resetKey, () => {
  if (chart && props.zoom) {
    chart.dispatchAction({ type: 'dataZoom', start: 0, end: 100 })
  }
})

onMounted(() => {
  chart = echarts.init(el.value)
  bindHover()
  render()
  ro = new ResizeObserver(() => chart && chart.resize())
  ro.observe(el.value)
})

watch(() => props.series, render, { deep: false })

onBeforeUnmount(() => {
  if (ro) ro.disconnect()
  if (chart) { chart.dispose(); chart = null }
})
</script>

<template>
  <div ref="el" :style="{ height, width: '100%' }"></div>
</template>
