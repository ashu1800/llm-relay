import { defineConfig } from 'vitest/config'

// 单元测试只测纯逻辑与组合式函数（.ts），不挂载组件 —— 不需要 vue 插件，
// 独立成文件也是为了不把 vite.config.ts 的 dev 代理等开发期配置带进测试环境。
// happy-dom 提供 localStorage / matchMedia / requestAnimationFrame，
// theme store 与 useCopyFeedback 的用例需要它们。
export default defineConfig({
  test: {
    environment: 'happy-dom',
    include: ['src/**/*.spec.ts'],
  },
})
