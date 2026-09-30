import {defineConfig} from 'vite'
import vue from '@vitejs/plugin-vue'

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [vue()],
  server: {
    host: '127.0.0.1',
    port: 5173,
    strictPort: true,
    // 开发脚本 scripts/dev.ps1 会把后端固定在 127.0.0.1:7840；
    // 前端仍用相对路径请求，这里把 API/SSE/媒体流代理到后端。
    proxy: {
      '/api': {target: 'http://127.0.0.1:7840', changeOrigin: true},
      '/s/': {target: 'http://127.0.0.1:7840', changeOrigin: true},
      '/t/': {target: 'http://127.0.0.1:7840', changeOrigin: true},
      '/d/': {target: 'http://127.0.0.1:7840', changeOrigin: true},
    },
  },
})
