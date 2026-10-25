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

export function fmtUptime(sec, locale = 'zh') {
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  const m = Math.floor((sec % 3600) / 60)
  if (locale === 'zh') {
    if (d > 0) return `${d}天${h}时${m}分`
    if (h > 0) return `${h}时${m}分`
    return `${m}分`
  }
  if (locale === 'ja') {
    if (d > 0) return `${d}日${h}時間${m}分`
    if (h > 0) return `${h}時間${m}分`
    return `${m}分`
  }
  if (d > 0) return `${d}d ${h}h ${m}m`
  if (h > 0) return `${h}h ${m}m`
  return `${m}m`
}

// 温度传感器 key 美化与解释（三语）
const TEMP_NAMES = {
  'package-thermal': { zh: '封装', en: 'Package', ja: 'パッケージ' },
  'bigcore0-thermal': { zh: '大核0', en: 'Big core 0', ja: 'ビッグコア0' },
  'bigcore2-thermal': { zh: '大核1', en: 'Big core 1', ja: 'ビッグコア1' },
  'littlecore-thermal': { zh: '小核', en: 'Little cores', ja: 'リトルコア' },
  'center-thermal': { zh: '中心', en: 'Center', ja: 'センター' },
  'gpu-thermal': { zh: 'GPU', en: 'GPU', ja: 'GPU' },
  'npu-thermal': { zh: 'NPU', en: 'NPU', ja: 'NPU' },
  '3:0:0:0': { zh: 'SATA控制器', en: 'SATA ctrl', ja: 'SATAコントローラ' },
  mmcblk0: { zh: 'eMMC', en: 'eMMC', ja: 'eMMC' },
}
const TEMP_TIPS = {
  zh: 'SoC 内部温度传感器；大核/小核指 CPU 集群；3:0:0:0 这类名称是 SATA 控制器（host:bus.target.lun 编号）的链路温度；sda 等设备名是硬盘盘体温度。',
  en: 'Internal SoC sensors; big/little cores are CPU clusters; names like 3:0:0:0 are the SATA controller (host:bus.target.lun) link temperature; device names like sda are drive temperatures.',
  ja: 'SoC 内部の温度センサー。ビッグ/リトルコアは CPU クラスタ。3:0:0:0 のような名前は SATA コントローラ（host:bus.target.lun）のリンク温度。sda などはドライブ本体温度。',
}

export function tempName(key, locale = 'zh') {
  const e = TEMP_NAMES[key]
  if (e) return e[locale] || e.zh
  if (key.endsWith('-thermal')) return key.replace(/-thermal$/, '')
  return key
}

export function tempTip(locale = 'zh') {
  return TEMP_TIPS[locale] || TEMP_TIPS.zh
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
export function fmtDur(sec, locale = 'zh') {
  const zh = locale === 'zh'
  const ja = locale === 'ja'
  const n = (x) => (Number.isInteger(x) ? String(x) : x.toFixed(1).replace(/\.0$/, ''))
  if (sec < 60) return zh || ja ? `${sec}秒` : `${sec}s`
  if (sec < 3600) {
    if (zh) return `${n(sec / 60)}分钟`
    if (ja) return `${n(sec / 60)}分`
    return `${n(sec / 60)}m`
  }
  if (sec < 86400) {
    if (zh) return `${n(sec / 3600)}小时`
    if (ja) return `${n(sec / 3600)}時間`
    return `${n(sec / 3600)}h`
  }
  if (zh || ja) return `${n(sec / 86400)}天`
  return `${n(sec / 86400)}d`
}
