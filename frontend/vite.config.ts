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
    // 阈值定在 900 kB 是实测结论，不是拿来消警告的：
    // 两个大 chunk 是 echarts（565 kB）与 antd 按需注册后的剩余部分（870 kB），
    // 都属于「第三方库、不随业务代码变化、可长期缓存」。业务代码最大的
    // ChannelsView 只有 71 kB。入口 chunk 已从全量注册时的 1541 kB 降到 12 kB。
    // 真要再降就得对 echarts 做按需引入（只引用的图表类型）或异步加载看板。
    chunkSizeWarningLimit: 900,
    // 入口 chunk 实测约 1.5 MB（minify 后），其中绝大部分是 antd 全量注册。
    // 这里把第三方库拆成独立的 vendor chunk：它们不随业务代码变化，
    // 浏览器可以长期缓存，业务改动只需要重新下载体积很小的 app chunk。
    rollupOptions: {
      output: {
        manualChunks: {
          vue: ['vue', 'vue-router', 'pinia'],
          antd: ['ant-design-vue'],
          echarts: ['echarts']
        }
      }
    }
  },
  server: {
    // 默认只绑回环：/api 代理直通**没有任何鉴权**的管理接口
    // （可读渠道密钥、请求日志、导出全部凭据密文）。
    // 需要局域网调试时用 `npm run dev -- --host` 显式打开。
    host: '127.0.0.1',
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8888',
        changeOrigin: true,
        // 必须显式声明 ws：Vite 的 upgrade 处理只在 opts.ws 为真时
        // 才把 WebSocket 交给代理，否则 /api/admin/live 的握手永远连不上。
        // 表现为开发态看板数字与日志不更新（生产态同源直连，不受影响），
        // 很容易被误判成「后端没推」。
        ws: true
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
