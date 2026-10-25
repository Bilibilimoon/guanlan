// API 封装：相对路径（兼容 fnOS 网关前缀 /app/guanlan/ 与直连两种部署）
let adminDenied = null
export function onAdminDenied(fn) { adminDenied = fn }

// fnOS 网关登录会话可能过期：首次 403 先静默重试一次，仍 403 才报「非管理员/已过期」，
// 并自动刷新一次页面（限一次，防循环）——刷新会重新走网关登录态，多数情况自愈。
let deniedFired = false

async function fetchWithRetry(path, opts) {
  let r = await fetch(path, opts)
  if (r.status === 403) {
    await new Promise((res) => setTimeout(res, 2000))
    r = await fetch(path, opts)
  }
  return r
}

function handle403() {
  if (deniedFired) return
  deniedFired = true
  const reloaded = sessionStorage.getItem('guanlan.403reload')
  if (!reloaded) {
    sessionStorage.setItem('guanlan.403reload', '1')
    location.reload()
    return
  }
  if (adminDenied) adminDenied()
  // 下一次正常成功响应后重置标记，允许未来的过期再次自愈
  sessionStorage.removeItem('guanlan.403reload')
}

async function doFetch(path, opts) {
  const r = await fetchWithRetry(path, opts)
  if (r.status === 403) { handle403(); throw new Error('HTTP 403') }
  if (!r.ok) throw new Error(`HTTP ${r.status}`)
  deniedFired = false
  return r.json()
}

export async function getJSON(path) {
  return doFetch(path, { headers: { 'X-Requested-With': 'guanlan' } })
}

export async function postJSON(path, body) {
  return doFetch(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'guanlan' },
    body: JSON.stringify(body),
  })
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
