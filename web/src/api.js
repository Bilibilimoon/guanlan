// API 封装：相对路径（兼容 fnOS 网关前缀 /app/guanlan/ 与直连两种部署）
let adminDenied = null
export function onAdminDenied(fn) { adminDenied = fn }

export async function getJSON(path) {
  const r = await fetch(path, { headers: { 'X-Requested-With': 'guanlan' } })
  if (r.status === 403 && adminDenied) adminDenied()
  if (!r.ok) throw new Error(`HTTP ${r.status}`)
  return r.json()
}

export async function postJSON(path, body) {
  const r = await fetch(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'guanlan' },
    body: JSON.stringify(body),
  })
  if (r.status === 403 && adminDenied) adminDenied()
  if (!r.ok) throw new Error(`HTTP ${r.status}`)
  return r.json()
}

// 轮询：页面切到后台自动暂停，回到前台恢复
export function poll(fn, intervalMs) {
  let timer = null
  let stopped = false
  const run = async () => {
    if (document.hidden) return
    try { await fn() } catch (e) { /* 单次失败不打断轮询 */ }
  }
  const start = () => {
    if (timer || stopped) return
    timer = setInterval(run, intervalMs)
    run()
  }
  const stop = () => { if (timer) { clearInterval(timer); timer = null } }
  document.addEventListener('visibilitychange', () => (document.hidden ? stop() : start()))
  start()
  return stop
}
