import { fileURLToPath } from 'node:url'
import { defineConfig } from 'vitest/config'

// 单元测试只测纯逻辑与组合式函数（.ts），不挂载组件 —— 不需要 vue 插件，
// 独立成文件也是为了不把 vite.config.ts 的 dev 代理等开发期配置带进测试环境。
// happy-dom 提供 localStorage / matchMedia / requestAnimationFrame，
// theme store 与 useCopyFeedback 的用例需要它们。
//
// 这里要**手工**把 @ 别名再写一遍：不继承 vite.config.ts 的代价就是它。
// 不写的话，任何 import 了 '@/…' 的模块在测试里都解析不了，而 src 下的文件
// 一律用 @/ 互相引用（2026-09-30 主题 store 引入 @/utils/themes 时踩到，
// 表现是「Failed to resolve import」）。
export default defineConfig({
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url))
    }
  },
  test: {
    environment: 'happy-dom',
    include: ['src/**/*.spec.ts'],
  },
})
