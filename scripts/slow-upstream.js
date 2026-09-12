// 慢速 SSE 上游，仅用于验证渠道并发名额的持有时间。
//
// 关键点是 /stats：它直接记录「同时进行的流式请求数」的峰值。
// 这比用总耗时推断可靠得多 —— 耗时里混着连接建立、容器调度等噪声，
// 而峰值并发数是「名额有没有被提前释放」的直接证据。
const http = require('http');
const HOLD_MS = Number(process.env.HOLD_MS || 2000);
const PORT = Number(process.env.PORT || 9999);

let current = 0;
let peak = 0;
let total = 0;

http.createServer((req, res) => {
  if (req.method === 'GET' && req.url.startsWith('/stats')) {
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ peak_concurrent: peak, current: current, total_streams: total }));
    return;
  }
  if (req.method === 'GET' && req.url.startsWith('/reset')) {
    peak = 0; total = 0;
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end('{"ok":true}');
    return;
  }

  let body = '';
  req.on('data', (c) => (body += c));
  req.on('end', () => {
    let p = {};
    try { p = JSON.parse(body); } catch (e) {}
    const usage = { prompt_tokens: 10, completion_tokens: 5, total_tokens: 15 };

    if (!p.stream) {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({
        id: 'slow', object: 'chat.completion', created: 0, model: p.model,
        choices: [{ index: 0, message: { role: 'assistant', content: 'ok' }, finish_reason: 'stop' }],
        usage,
      }));
      return;
    }

    current++;
    total++;
    if (current > peak) peak = current;

    // 模型名里带 truncate 时模拟「上游中途挂掉」：
    // 发两个分片后直接掐断 TCP 连接，不补结束标记。
    // 用来验证中继不会把截断的回复伪装成正常结束。
    const TRUNCATE = String(p.model || '').includes('truncate');

    res.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache' });
    const N = 4;
    const gap = Math.max(50, Math.floor(HOLD_MS / N));
    let i = 0;
    const timer = setInterval(() => {
      i++;
      if (TRUNCATE && i > 2) {
        clearInterval(timer);
        current--;
        res.socket.destroy();
        return;
      }
      const last = i >= N;
      const chunk = {
        id: 'slow', object: 'chat.completion.chunk', created: 0, model: p.model,
        choices: [{ index: 0, delta: last ? {} : { content: 'tok' + i }, finish_reason: last ? 'stop' : null }],
      };
      if (last) chunk.usage = usage;
      res.write('data: ' + JSON.stringify(chunk) + '\n\n');
      if (last) {
        res.write('data: [DONE]\n\n');
        clearInterval(timer);
        current--;
        res.end();
      }
    }, gap);
  });
}).listen(PORT, '0.0.0.0', () => console.log('slow upstream on ' + PORT + ', hold=' + HOLD_MS + 'ms'));
