// Anthropic 原生协议的上游 mock，用来验证「渠道协议 = 上游协议」这条链路。
//
// 它的关键作用是**当裁判**：如果中转站没有把请求转成 Anthropic 形状就发过来
// （带 stream_options、system 还在 messages 里、缺 max_tokens），
// 这里会按真实 Anthropic 的行为返回 400 —— 也就是说「没转换」这件事会直接变成
// 可见的失败，而不是被一个宽容的 mock 掩盖过去。
//
// 同时记录收到的最后一个请求体与命中路径，供用例断言「发出去的确实是 /v1/messages」。
const http = require('http');
const PORT = Number(process.env.PORT || 9998);

let anthropicHits = 0;
let openaiHits = 0;
let geminiHits = 0;
let rejected = 0;
let lastRequest = null;
let lastPath = '';
let lastModel = '';

// 真实 Anthropic 对未知字段、缺失必填字段都是直接 400
function validateAnthropic(p, rawKeys) {
  if (p.max_tokens === undefined || p.max_tokens === null) {
    return 'max_tokens: Field required';
  }
  if (rawKeys.includes('stream_options')) {
    return 'stream_options: Extra inputs are not permitted';
  }
  for (const k of ['presence_penalty', 'frequency_penalty', 'n', 'logprobs', 'response_format', 'user']) {
    if (rawKeys.includes(k)) return k + ': Extra inputs are not permitted';
  }
  if (!Array.isArray(p.messages)) return 'messages: Field required';
  for (const m of p.messages) {
    if (m.role === 'system') return 'Unexpected role "system": use the top-level system field';
    if (m.role === 'tool' || m.role === 'function') return 'Unexpected role "' + m.role + '"';
    if (typeof m.content === 'string') {
      return 'content: Input should be a valid list';
    }
  }
  return null;
}

function errorBody(type, message) {
  return JSON.stringify({ type: 'error', error: { type, message } });
}

const server = http.createServer((req, res) => {
  if (req.method === 'GET' && req.url.startsWith('/stats')) {
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ anthropic_hits: anthropicHits, openai_hits: openaiHits, gemini_hits: geminiHits, rejected, last_path: lastPath, last_request: lastRequest, last_model: lastModel }));
    return;
  }
  if (req.method === 'GET' && req.url.startsWith('/reset')) {
    anthropicHits = 0; openaiHits = 0; geminiHits = 0; rejected = 0; lastRequest = null; lastPath = ''; lastModel = '';
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end('{"ok":true}');
    return;
  }

  let body = '';
  req.on('data', (c) => (body += c));
  req.on('end', () => {
    lastPath = req.url;
    let p = {};
    try { p = JSON.parse(body); } catch (e) { p = {}; }
    lastRequest = p;

    // ---------- Gemini generateContent ----------
    if (req.url.startsWith('/v1beta/models/')) {
      geminiHits++;
      const m = req.url.match(/\/v1beta\/models\/([^:?]+):([A-Za-z]+)/);
      const model = m ? decodeURIComponent(m[1]) : '';
      const method = m ? m[2] : '';
      lastModel = model;
      const badField = ['messages', 'max_tokens', 'stream_options', 'stream', 'temperature', 'top_p']
        .find((k) => p[k] !== undefined);
      if (badField) {
        rejected++;
        res.writeHead(400, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ error: { code: 400, message: 'Unknown name "' + badField + '": Cannot find field.', status: 'INVALID_ARGUMENT' } }));
        return;
      }
      if (!Array.isArray(p.contents)) {
        rejected++;
        res.writeHead(400, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ error: { code: 400, message: 'contents: Field required', status: 'INVALID_ARGUMENT' } }));
        return;
      }
      if (p.contents.some((c) => c && c.role === 'system')) {
        rejected++;
        res.writeHead(400, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ error: { code: 400, message: '请用 systemInstruction 传系统提示', status: 'INVALID_ARGUMENT' } }));
        return;
      }
      if (model.includes('trigger-error')) {
        rejected++;
        res.writeHead(400, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ error: { code: 400, message: 'mock 拒绝了这个模型：' + model, status: 'INVALID_ARGUMENT' } }));
        return;
      }
      if (method === 'streamGenerateContent') {
        // 真实 Gemini 不带 alt=sse 时返回的是 JSON 数组（不是 SSE），
        // 这里照做：中转站若漏了 alt=sse，客户端拿到的就不是分片流
        if (!req.url.includes('alt=sse')) {
          res.writeHead(200, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify([{ candidates: [{ content: { role: 'model', parts: [{ text: 'not-sse' }] }, finishReason: 'STOP', index: 0 }], modelVersion: model }]));
          return;
        }
        res.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache' });
        const frame = (o) => res.write('data: ' + JSON.stringify(o) + '\n\n');
        frame({ candidates: [{ content: { role: 'model', parts: [{ text: 'hello ' }] }, index: 0 }], usageMetadata: { promptTokenCount: 10, candidatesTokenCount: 1, totalTokenCount: 11 }, modelVersion: model });
        frame({ candidates: [{ content: { role: 'model', parts: [{ text: 'from gemini' }] }, index: 0 }], usageMetadata: { promptTokenCount: 10, candidatesTokenCount: 3, totalTokenCount: 13 }, modelVersion: model });
        frame({ candidates: [{ content: { role: 'model', parts: [] }, finishReason: 'STOP', index: 0 }], usageMetadata: { promptTokenCount: 10, candidatesTokenCount: 3, totalTokenCount: 13 }, modelVersion: model });
        res.end();
        return;
      }
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({
        candidates: [{ content: { role: 'model', parts: [{ text: 'hello from gemini' }] }, finishReason: 'STOP', index: 0 }],
        usageMetadata: { promptTokenCount: 10, candidatesTokenCount: 6, totalTokenCount: 16, cachedContentTokenCount: 2 },
        modelVersion: model,
      }));
      return;
    }

    // 用到 OpenAI 的端点就是走错了协议（中转站没转换）
    if (req.url.startsWith('/v1/chat/completions')) {
      openaiHits++;
      res.writeHead(400, { 'Content-Type': 'application/json' });
      res.end(errorBody('invalid_request_error', '这个上游只提供 /v1/messages'));
      return;
    }

    anthropicHits++;
    // 用例需要一个「上游确实拒绝」的场景：模型名带上 trigger-error 就报错
    if (String(p.model || '').includes('trigger-error')) {
      rejected++;
      res.writeHead(400, { 'Content-Type': 'application/json' });
      res.end(errorBody('invalid_request_error', 'mock 拒绝了这个模型：' + p.model));
      return;
    }
    const problem = validateAnthropic(p, Object.keys(p));
    if (problem) {
      rejected++;
      res.writeHead(400, { 'Content-Type': 'application/json' });
      res.end(errorBody('invalid_request_error', problem));
      return;
    }

    // 让用例能验证「上游名替换成了白名单里的映射名」
    const echo = p.model || '';
    if (p.stream) {
      res.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache' });
      const send = (event, data) => res.write('event: ' + event + '\ndata: ' + JSON.stringify(data) + '\n\n');
      send('message_start', { type: 'message_start', message: { id: 'msg_mock', type: 'message', role: 'assistant', model: echo, content: [], usage: { input_tokens: 12, output_tokens: 1, cache_read_input_tokens: 3 } } });
      send('content_block_start', { type: 'content_block_start', index: 0, content_block: { type: 'text', text: '' } });
      send('content_block_delta', { type: 'content_block_delta', index: 0, delta: { type: 'text_delta', text: 'hello ' } });
      send('content_block_delta', { type: 'content_block_delta', index: 0, delta: { type: 'text_delta', text: 'from anthropic' } });
      send('content_block_stop', { type: 'content_block_stop', index: 0 });
      send('message_delta', { type: 'message_delta', delta: { stop_reason: 'end_turn', stop_sequence: null }, usage: { output_tokens: 7 } });
      send('message_stop', { type: 'message_stop' });
      res.end();
      return;
    }

    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({
      id: 'msg_mock', type: 'message', role: 'assistant', model: echo,
      content: [{ type: 'text', text: 'hello from anthropic' }],
      stop_reason: 'end_turn', stop_sequence: null,
      usage: { input_tokens: 12, output_tokens: 7, cache_read_input_tokens: 3, cache_creation_input_tokens: 4 },
    }));
  });
});

server.listen(PORT, () => console.log('proto-upstream listening on ' + PORT));
