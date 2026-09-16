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
    // 现在最大的 chunk 是 antd 按需注册后的剩余部分（约 870 kB），
    // 属于「第三方库、不随业务代码变化、可长期缓存」。业务代码最大的
    // ChannelsView 只有 71 kB。入口 chunk 已从全量注册时的 1541 kB 降到 12 kB。
    // （2026-09-16：看板的四张图表与热力图移除后，echarts 这个 565 kB 的
    //   chunk 连同依赖一起删掉了 —— 阈值与分包表都跟着它一起收窄。）
    chunkSizeWarningLimit: 900,
    // 入口 chunk 实测约 1.5 MB（minify 后），其中绝大部分是 antd 全量注册。
    // 这里把第三方库拆成独立的 vendor chunk：它们不随业务代码变化，
    // 浏览器可以长期缓存，业务改动只需要重新下载体积很小的 app chunk。
    rollupOptions: {
      output: {
        manualChunks: {
          vue: ['vue', 'vue-router', 'pinia'],
          antd: ['ant-design-vue']
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
        ws: true,
        // Origin 也要改写成后端自己的源，否则管理接口一律 403。
        //
        // 后端有同源校验（middleware.sameOriginOnly：Origin 的 host 必须等于
        // 请求的 Host），而 changeOrigin 只改 Host —— 浏览器发来的 Origin 仍是
        // http://127.0.0.1:5173，两个 host 对不上就拒绝。
        // 为什么以前没暴露：同源 GET 不带 Origin 头，所以「读」一直是好的；
        // 带 Origin 的是**写请求**与 **WebSocket 握手** ——
        // 于是开发态的表现是「页面能看、保存失败、实时不更新」（实测
        // /api/admin/live 握手返回 403，控制台里只有一句 handshake 失败）。
        // 生产态不经过这个代理，后端那行校验一个字没改。
        headers: { Origin: 'http://127.0.0.1:8888' }
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
