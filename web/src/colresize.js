import { ref } from 'vue'

// 可复用的表格列宽拖动（Excel 式）。
// 关键实现：拖动过程中直接改 <col> 元素的 style.width（DOM 立即生效，
// 不依赖 Vue 响应式链路——绕开"拖了没反应"的一切可能原因）；
// 松手后才把最终宽度写回 state + localStorage。
// key 用于持久化；defaults 为 {列key: 初始宽度px}。
export function useColResize(key, defaults) {
  let saved = {}
  try { saved = JSON.parse(localStorage.getItem('guanlan.colw.' + key)) || {} } catch (e) { /* 忽略 */ }
  const widths = ref({ ...defaults, ...saved })

  function persist() {
    localStorage.setItem('guanlan.colw.' + key, JSON.stringify(widths.value))
  }

  const EDGE = 12 // 表头右缘命中区宽度

  // 表头鼠标移动：右缘显示 col-resize 光标（让"能拖"可见）
  function thCursor(_col, e) {
    const th = e.currentTarget
    const r = th.getBoundingClientRect()
    th.style.cursor = e.clientX >= r.right - EDGE && e.clientX <= r.right + 2 ? 'col-resize' : ''
  }

  // 表头按下：命中右缘则开始拖宽；返回是否命中（未命中不拦截，留给排序等）
  function thResize(col, e) {
    const th = e.currentTarget
    const r = th.getBoundingClientRect()
    if (e.clientX < r.right - EDGE || e.clientX > r.right + 2) return false
    e.preventDefault()
    e.stopPropagation()

    // 定位同列的 <col> 元素（拖动直接改它，立即生效）
    const table = th.closest('table')
    const cols = table ? table.querySelectorAll('colgroup col') : []
    const idx = Array.prototype.indexOf.call(th.parentElement.children, th)
    const colEl = cols[idx]

    const startX = e.clientX
    const startW = r.width
    const moved = (ev) => {
      const w = Math.max(40, startW + ev.clientX - startX)
      if (colEl) colEl.style.width = w + 'px'
      widths.value = { ...widths.value, [col]: w }
    }
    const up = () => {
      document.removeEventListener('pointermove', moved)
      document.removeEventListener('pointerup', up)
      document.body.style.userSelect = ''
      persist()
    }
    document.body.style.userSelect = 'none'
    document.addEventListener('pointermove', moved)
    document.addEventListener('pointerup', up)
    return true
  }

  function reset() {
    widths.value = { ...defaults }
    persist()
  }

  return { widths, thResize, thCursor, reset }
}
