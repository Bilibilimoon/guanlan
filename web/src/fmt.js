// 数值格式化工具
export function fmtBytes(n) {
  if (!isFinite(n)) return '-'
  if (n < 1024) return `${n.toFixed(0)} B`
  const units = ['KB', 'MB', 'GB', 'TB', 'PB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return `${v >= 100 ? v.toFixed(0) : v.toFixed(1)} ${units[i]}`
}

export function fmtBps(n) { return fmtBytes(n) + '/s' }

export function fmtGiB(bytes) {
  return (bytes / 1024 ** 3).toFixed(1) + ' GiB'
}

export function fmtPct(n) { return `${n.toFixed(1)}%` }

export function fmtC(n) { return `${n.toFixed(1)}°C` }

export function fmtUptime(sec, zh = true) {
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  const m = Math.floor((sec % 3600) / 60)
  if (zh) {
    if (d > 0) return `${d}天${h}时${m}分`
    if (h > 0) return `${h}时${m}分`
    return `${m}分`
  }
  if (d > 0) return `${d}d ${h}h ${m}m`
  if (h > 0) return `${h}h ${m}m`
  return `${m}m`
}

export function fmtClock(ts) {
  const d = new Date(ts * 1000)
  const p = (x) => String(x).padStart(2, '0')
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

// 毫秒时间戳（ECharts time 轴原生域）
export function fmtClockMs(ms) {
  const d = new Date(ms)
  const p = (x) => String(x).padStart(2, '0')
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

export function fmtFullMs(ms) {
  const d = new Date(ms)
  const p = (x) => String(x).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

// X 轴刻度：按数据跨度自适应 显示到秒 / 时分 / 带日期
export function fmtAxisMs(ms, spanSec) {
  const d = new Date(ms)
  const p = (x) => String(x).padStart(2, '0')
  const hm = `${p(d.getHours())}:${p(d.getMinutes())}`
  if (spanSec <= 300) return `${hm}:${p(d.getSeconds())}`
  if (spanSec < 43200) return hm
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${hm}`
}

export function fmtRPM(n) { return `${Math.round(n)} RPM` }

// 压力色：0(轻)→绿，100(重)→红，中间经黄（HSL 连续插值）
export function pressureColor(p) {
  if (!isFinite(p)) p = 0
  p = Math.min(100, Math.max(0, p))
  const hue = p <= 50 ? 140 - 92 * (p / 50) : 48 - 48 * ((p - 50) / 50)
  return `hsl(${hue.toFixed(0)}, 62%, 48%)`
}

// 窗口时长（秒）显示为本地化短文本
export function fmtDur(sec, zh = true) {
  const n = (x) => (Number.isInteger(x) ? String(x) : x.toFixed(1).replace(/\.0$/, ''))
  if (sec < 60) return `${sec}${zh ? '秒' : 's'}`
  if (sec < 3600) return `${n(sec / 60)}${zh ? '分钟' : 'm'}`
  if (sec < 86400) return `${n(sec / 3600)}${zh ? '小时' : 'h'}`
  return `${n(sec / 86400)}${zh ? '天' : 'd'}`
}
