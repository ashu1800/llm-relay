import { describe, expect, it } from 'vitest'
import { parseModelName } from './modelName'

describe('parseModelName', () => {
  it('解析带厂商前缀与日期后缀的标准模型', () => {
    expect(parseModelName('anthropic/claude-3-7-sonnet-20250219')).toEqual({
      prefix: 'anthropic/',
      core: 'claude-3-7-sonnet',
      suffix: '-20250219'
    })
  })

  it('解析带供应商前缀与 preview 后缀的模型', () => {
    expect(parseModelName('google/gemini-2.5-pro-preview')).toEqual({
      prefix: 'google/',
      core: 'gemini-2.5-pro',
      suffix: '-preview'
    })
  })

  it('解析纯核心名模型', () => {
    expect(parseModelName('gpt-4o')).toEqual({
      prefix: undefined,
      core: 'gpt-4o',
      suffix: undefined
    })
  })

  it('解析带前缀但无后缀的模型', () => {
    expect(parseModelName('deepseek-ai/DeepSeek-V3')).toEqual({
      prefix: 'deepseek-ai/',
      core: 'DeepSeek-V3',
      suffix: undefined
    })
  })

  it('解析带 latest 后缀的模型', () => {
    expect(parseModelName('chatgpt-4o-latest')).toEqual({
      prefix: undefined,
      core: 'chatgpt-4o',
      suffix: '-latest'
    })
  })

  it('安全处理空字符串与非字符串异常输入', () => {
    expect(parseModelName('')).toEqual({ core: '' })
    expect(parseModelName(null as unknown as string)).toEqual({ core: '' })
    expect(parseModelName(undefined as unknown as string)).toEqual({ core: '' })
  })
})
