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

  function reset() {
    widths.value = { ...defaults }
    persist()
  }

  return { widths, startResize, reset }
}
