<script setup>
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import * as echarts from 'echarts/core'
import { LineChart } from 'echarts/charts'
import { GridComponent, LegendComponent, TooltipComponent, DataZoomInsideComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import { fmtBytes, fmtClockMs, fmtFullMs, fmtAxisMs } from '../fmt'

// 按需引入：dataZoom 也要注册，否则 inside 型 dataZoom 的配置不报错但不生效（滚轮缩不了）
echarts.use([LineChart, GridComponent, LegendComponent, TooltipComponent, DataZoomInsideComponent, CanvasRenderer])

const props = defineProps({
  // [{ name, color, points: [[unixSec, value], ...] }]（秒，进图前统一转毫秒）
  series: { type: Array, default: () => [] },
  // percent | celsius | bytes | rpm | watt | count
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
let roOutside = null // 图表外按下的全局监听（卸载时移除）
let lastPos = null // 悬停中的像素坐标（canvas 系）：仅鼠标悬停记录；数据刷新后据此恢复 tooltip

function fmtVal(v) {
  switch (props.yType) {
    case 'percent': return `${v.toFixed(1)}%`
    case 'celsius': return `${v.toFixed(1)}°C`
    case 'bytes': return fmtBytes(v) + '/s'
    case 'rpm': return `${Math.round(v)} RPM`
    case 'watt': return `${v.toFixed(1)} W`
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
    case 'watt': return { ...base, scale: true, axisLabel: { ...base.axisLabel, formatter: (v) => `${v}W` } }
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
    // replaceMerge：只替换 series / yAxis，保留 tooltip / axisPointer 等组件状态——
    // 数据刷新不打断悬停中的 tooltip；yAxis 一并替换，避免切换分组后
    // 上一组残留的 max/min（如百分比组的 max:100）粘在功耗等自适应轴上
  }, { replaceMerge: ['series', 'yAxis'] })

  // 兜底：若 tooltip 仍被隐藏（如组件重建），按悬停像素坐标恢复
  if (lastPos) {
    const pos = lastPos
    requestAnimationFrame(() => {
      chart && chart.dispatchAction({ type: 'showTip', x: pos.x, y: pos.y })
    })
  }
}

function bindHover() {
  const elRef = el.value
  // 鼠标：记录悬停位置（数据刷新后据此恢复 tooltip）
  elRef.addEventListener('pointermove', (e) => {
    if (e.pointerType === 'mouse') lastPos = { x: e.offsetX, y: e.offsetY }
  })
  elRef.addEventListener('pointerleave', () => { lastPos = null })

  // 触摸：抬起/取消即隐藏浮窗（触摸端没有 globalout，ECharts 默认会一直挂着）；
  // 触摸不记录 lastPos，数据刷新也不会把浮窗"恢复"出来
  const hideTip = () => {
    lastPos = null
    if (chart) chart.dispatchAction({ type: 'hideTip' })
  }
  elRef.addEventListener('pointerup', (e) => { if (e.pointerType !== 'mouse') hideTip() })
  elRef.addEventListener('pointercancel', hideTip)

  // 手指/鼠标点到图表之外：隐藏遗留浮窗
  roOutside = (e) => {
    if (!elRef.contains(e.target)) hideTip()
  }
  window.addEventListener('pointerdown', roOutside, true)

  if (props.zoom) {
    chart.on('dataZoom', () => {
      const dz = chart.getOption().dataZoom
      if (!dz || !dz[0]) return
      let s = dz[0].startValue
      let e = dz[0].endValue
      if (!isFinite(s) || !isFinite(e)) {
        // dataZoom 用百分比（start/end）驱动时 startValue/endValue 是空的，
        // 按当前数据的时间范围换算成秒，避免上抛 NaN 导致非法查询
        let mn = Infinity
        let mx = -Infinity
        for (const ser of props.series) {
          for (const p of ser.points) {
            if (p[0] < mn) mn = p[0]
            if (p[0] > mx) mx = p[0]
          }
        }
        if (!isFinite(mn) || mx <= mn) return
        const p0 = isFinite(dz[0].start) ? dz[0].start : 0
        const p1 = isFinite(dz[0].end) ? dz[0].end : 100
        s = mn + ((mx - mn) * p0) / 100
        e = mn + ((mx - mn) * p1) / 100
      } else if (s > 1e11) {
        // 个别情况下 startValue 是毫秒
        s /= 1000
        e /= 1000
      }
      if (!isFinite(s) || !isFinite(e) || e <= s) return
      emit('viewchange', { startSec: s, endSec: e })
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
  if (roOutside) window.removeEventListener('pointerdown', roOutside, true)
  if (ro) ro.disconnect()
  if (chart) { chart.dispose(); chart = null }
})
</script>

<template>
  <div ref="el" :style="{ height, width: '100%' }"></div>
</template>
