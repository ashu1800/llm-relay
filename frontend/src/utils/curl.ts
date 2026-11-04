export type CurlBuildOptions = {
  origin: string
  protocol: string
  model: string
  body?: string | null
}

/**
 * 拼装可直接在终端复现排障的完整 cURL 命令。
 *
 * 若原始报文未留存（如 errors 模式下的成功日志），自动生成对应协议的最小 Prompt 骨架
 * 并附带注释说明，确保复制出的命令在终端里立即可运行。
 */
export function buildCurlCommand(opts: CurlBuildOptions): string {
  const { origin, protocol, model, body } = opts
  const baseUrl = (origin || '').replace(/\/+$/, '')

  let path = '/v1/chat/completions'
  const headers: string[] = ['-H "Content-Type: application/json"']
  let fallbackBody = JSON.stringify(
    {
      model: model || 'gpt-4o',
      messages: [{ role: 'user', content: 'Hello' }]
    },
    null,
    2
  )

  switch (protocol) {
    case 'anthropic-messages':
      path = '/v1/messages'
      headers.push('-H "x-api-key: <YOUR_API_KEY>"')
      headers.push('-H "anthropic-version: 2023-06-01"')
      fallbackBody = JSON.stringify(
        {
          model: model || 'claude-3-7-sonnet',
          max_tokens: 1024,
          messages: [{ role: 'user', content: 'Hello' }]
        },
        null,
        2
      )
      break
    case 'gemini-generateContent':
      path = `/v1beta/models/${encodeURIComponent(model || 'gemini-2.0-flash')}:generateContent`
      headers.push('-H "x-goog-api-key: <YOUR_API_KEY>"')
      fallbackBody = JSON.stringify(
        {
          contents: [{ parts: [{ text: 'Hello' }] }]
        },
        null,
        2
      )
      break
    case 'openai-responses':
      path = '/v1/responses'
      headers.push('-H "Authorization: Bearer <YOUR_API_KEY>"')
      fallbackBody = JSON.stringify(
        {
          model: model || 'gpt-4o',
          input: 'Hello'
        },
        null,
        2
      )
      break
    case 'openai-embeddings':
      path = '/v1/embeddings'
      headers.push('-H "Authorization: Bearer <YOUR_API_KEY>"')
      fallbackBody = JSON.stringify(
        {
          model: model || 'text-embedding-3-small',
          input: 'Hello'
        },
        null,
        2
      )
      break
    case 'openai-images':
      path = '/v1/images/generations'
      headers.push('-H "Authorization: Bearer <YOUR_API_KEY>"')
      fallbackBody = JSON.stringify(
        {
          model: model || 'dall-e-3',
          prompt: 'A sunset over mountains',
          n: 1,
          size: '1024x1024'
        },
        null,
        2
      )
      break
    case 'openai-audio-speech':
      path = '/v1/audio/speech'
      headers.push('-H "Authorization: Bearer <YOUR_API_KEY>"')
      fallbackBody = JSON.stringify(
        {
          model: model || 'tts-1',
          input: 'Hello',
          voice: 'alloy'
        },
        null,
        2
      )
      break
    case 'openai-audio-transcriptions':
    case 'openai-audio-translations': {
      // multipart 端点不能拼 -d：文件要走 -F。无论报文是否留存都给可运行的
      // -F 形态 —— 报文里的 multipart 正文原样贴进 -d 是发不出去的
      //（boundary 与文件都是二进制）
      const audioPath =
        protocol === 'openai-audio-translations' ? '/v1/audio/translations' : '/v1/audio/transcriptions'
      const note = (opts.body || '').trim()
        ? '# 注：multipart 报文不便原样复现，请替换为你的音频文件'
        : '# 注：该请求未留存原始报文，已填充最小可运行形态（替换音频文件路径）'
      return [
        `curl -X POST "${baseUrl}${audioPath}"`,
        `  -H "Authorization: Bearer <YOUR_API_KEY>"`,
        `  -F model="${model || 'whisper-1'}" \\`,
        `  -F file="@your-audio.mp3"`,
        `  ${note}`
      ].join(' \\\n')
    }
    default:
      headers.push('-H "Authorization: Bearer <YOUR_API_KEY>"')
      break
  }

  const url = `${baseUrl}${path}`
  let comment = ''
  let payloadStr = (body || '').trim()

  if (!payloadStr) {
    comment = '# 注：该请求未留存原始报文，已填充最小测试请求体\n'
    payloadStr = fallbackBody
  }

  const escapedData = payloadStr.replace(/'/g, `'\\''`)

  const parts = [
    `${comment}curl -X POST "${url}"`,
    ...headers.map((h) => `  ${h}`),
    `  -d '${escapedData}'`
  ]

  return parts.join(' \\\n')
}
