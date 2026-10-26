import { describe, expect, it } from 'vitest'
import { buildCurlCommand } from './curl'

describe('buildCurlCommand', () => {
  it('生成标准 OpenAI Chat 协议的 cURL', () => {
    const cmd = buildCurlCommand({
      origin: 'http://127.0.0.1:8888',
      protocol: 'openai-chat',
      model: 'gpt-4o',
      body: '{"model":"gpt-4o","messages":[{"role":"user","content":"Hello"}]}'
    })
    expect(cmd).toContain('curl -X POST "http://127.0.0.1:8888/v1/chat/completions"')
    expect(cmd).toContain('-H "Authorization: Bearer <YOUR_API_KEY>"')
    expect(cmd).toContain('-H "Content-Type: application/json"')
    expect(cmd).toContain('-d \'{"model":"gpt-4o","messages":[{"role":"user","content":"Hello"}]}\'')
  })

  it('生成 Anthropic Messages 协议的 cURL', () => {
    const cmd = buildCurlCommand({
      origin: 'https://relay.example.com',
      protocol: 'anthropic-messages',
      model: 'claude-3-7-sonnet',
      body: '{"model":"claude-3-7-sonnet","messages":[{"role":"user","content":"Hello"}],"max_tokens":1024}'
    })
    expect(cmd).toContain('curl -X POST "https://relay.example.com/v1/messages"')
    expect(cmd).toContain('-H "x-api-key: <YOUR_API_KEY>"')
    expect(cmd).toContain('-H "anthropic-version: 2023-06-01"')
  })

  it('生成 Gemini generateContent 协议的 cURL', () => {
    const cmd = buildCurlCommand({
      origin: 'http://127.0.0.1:8888',
      protocol: 'gemini-generateContent',
      model: 'gemini-2.0-flash',
      body: '{"contents":[{"parts":[{"text":"Hi"}]}]}'
    })
    expect(cmd).toContain('curl -X POST "http://127.0.0.1:8888/v1beta/models/gemini-2.0-flash:generateContent"')
    expect(cmd).toContain('-H "x-goog-api-key: <YOUR_API_KEY>"')
  })

  it('当报文未留存（body 为空）时自动注入测试 Prompt 骨架与注释', () => {
    const cmd = buildCurlCommand({
      origin: 'http://127.0.0.1:8888',
      protocol: 'openai-chat',
      model: 'deepseek-chat',
      body: ''
    })
    expect(cmd).toContain('# 注：该请求未留存原始报文，已填充最小测试请求体')
    expect(cmd).toContain('"model": "deepseek-chat"')
    expect(cmd).toContain('"messages"')
  })
})
