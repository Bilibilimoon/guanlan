import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// base 用相对路径：fnOS 统一网关下页面挂在 /app/guanlan/ 前缀下也能工作
export default defineConfig({
  base: './',
  plugins: [vue()],
  server: {
    proxy: { '/api': 'http://127.0.0.1:9780' },
  },
})
