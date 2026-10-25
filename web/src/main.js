import { createApp } from 'vue'
import { createI18n } from 'vue-i18n'
import App from './App.vue'
import { messages } from './i18n'

// 白屏兜底：把未捕获的运行时错误显示出来，而不是静默空白
window.addEventListener('error', (e) => {
  const el = document.createElement('div')
  el.style.cssText = 'position:fixed;bottom:0;left:0;right:0;z-index:9999;padding:8px 12px;background:#5c1d1d;color:#ffb4ad;font:12px/1.5 monospace;white-space:pre-wrap'
  el.textContent = `观澜运行错误：${e.message} (${e.filename}:${e.lineno})`
  document.body.appendChild(el)
})

function detectLang() {
  const saved = localStorage.getItem('guanlan.lang')
  if (saved === 'zh' || saved === 'en') return saved
  return (navigator.language || 'zh').toLowerCase().startsWith('zh') ? 'zh' : 'en'
}

const i18n = createI18n({
  legacy: false,
  locale: detectLang(),
  fallbackLocale: 'en',
  messages,
})

createApp(App).use(i18n).mount('#app')
