import { ref } from 'vue'

// 可复用的表格列宽拖动（Excel 式）：返回宽度表与把手拖动入口。
// key 用于 localStorage 持久化；defaults 为 [列key -> 初始宽度px]。
export function useColResize(key, defaults) {
  let saved = {}
  try { saved = JSON.parse(localStorage.getItem('guanlan.colw.' + key)) || {} } catch (e) { /* 忽略 */ }
  const widths = ref({ ...defaults, ...saved })

  function persist() {
    localStorage.setItem('guanlan.colw.' + key, JSON.stringify(widths.value))
  }

  function startResize(col, e) {
    e.preventDefault()
    e.stopPropagation()
    const startX = e.clientX
    const startW = widths.value[col] || defaults[col] || 100
    const min = 40
    const move = (ev) => {
      widths.value = { ...widths.value, [col]: Math.max(min, startW + ev.clientX - startX) }
    }
    const up = () => {
      document.removeEventListener('pointermove', move)
      document.removeEventListener('pointerup', up)
      persist()
    }
    document.addEventListener('pointermove', move)
    document.addEventListener('pointerup', up)
  }

  // 表头边缘检测：按下点位于表头右缘 EDGE px 内即进入列宽调整。
  // 比依赖 7px 小把手可靠得多（用户痛点：把手拖了没反应）。
  const EDGE = 10
  function thResize(col, e) {
    const r = e.currentTarget.getBoundingClientRect()
    if (e.clientX < r.right - EDGE) return false // 未命中边缘：交给其他逻辑（排序等）
    startResize(col, e)
    return true
  }

  function reset() {
    widths.value = { ...defaults }
    persist()
  }

  return { widths, startResize, thResize, reset }
}
