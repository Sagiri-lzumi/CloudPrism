import {defineConfig} from 'vite'
import vue from '@vitejs/plugin-vue'

// 后端地址由开发脚本 scripts/dev.ps1 探测实际监听端口后经 CP_BACKEND_ORIGIN
// 注入：后端端口可配（data/config.json）且被占用时顺延，写死 7840 会在端口
// 变动时把整个代理打到错误的实例上（例如 run.ps1 跑着的那个）。缺省值只作
// 文档性兜底，正常流程不该走到。
const backendOrigin = process.env.CP_BACKEND_ORIGIN ?? 'http://127.0.0.1:7840'

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [vue()],
  server: {
    host: '127.0.0.1',
    port: 5173,
    strictPort: true,
    // 前端仍用相对路径请求，这里把 API/SSE/媒体流代理到后端实际端口。
    proxy: {
      '/api': {target: backendOrigin, changeOrigin: true},
      '/s/': {target: backendOrigin, changeOrigin: true},
      '/t/': {target: backendOrigin, changeOrigin: true},
      '/d/': {target: backendOrigin, changeOrigin: true},
    },
  },
})
