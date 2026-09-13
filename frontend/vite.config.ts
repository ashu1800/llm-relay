import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 前端产物交给 Go 后端 embed，开发态通过代理直连后端
export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url))
    }
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    chunkSizeWarningLimit: 1500
  },
  server: {
    host: '0.0.0.0',
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8888',
        changeOrigin: true
      },
      // 中转端点一并代理：密钥页会把「当前源 + /v1」当成客户端该填的 BaseURL 显示出来，
      // 开发态下这个地址要真的通，否则复制到客户端里拿到的是 vite 的 index.html
      '/v1': {
        target: 'http://127.0.0.1:8888',
        changeOrigin: true
      },
      '/v1beta': {
        target: 'http://127.0.0.1:8888',
        changeOrigin: true
      }
    }
  }
})
