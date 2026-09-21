// 可编程的「会失败的上游」，用于验证故障转移的退避行为。
//
// 与 slow-upstream.js 的区别：那个只会成功（用来验并发名额的持有时间），
// 这个能**按序返回指定的错误状态码**，并记录**每次请求到达的时间戳**。
//
// 为什么要记时间戳而不是只看总耗时：总耗时里混着连接建立、容器调度、
// 上游生成等噪声，无法用来判断「两次尝试之间等了多久」。相邻两次到达的
// 间隔才是退避是否生效的直接证据 —— 与 slow-upstream.js 用并发峰值而不是
// 耗时来证明名额语义是同一个判据思想。
//
// 控制接口：
//   POST /fail  {"status":503,"times":2,"retry_after":1}
//        前 times 次请求返回 status（默认 503），之后一律 200。
//        retry_after 非 0 时带上 Retry-After 头（秒）。
//   POST /reset 清空计数与到达记录
//   GET  /stats {"arrivals_ms":[...], "gaps_ms":[...], "hits":N}
//        gaps_ms 是相邻两次到达的间隔，测试直接断言它
const http = require('http');
const PORT = Number(process.env.PORT || 9996);

let arrivals = [];
let hits = 0;
let failStatus = 0;   // 0 = 不失败
let failTimes = 0;    // 前几次失败
let retryAfter = 0;   // 秒；0 = 不带该头

function send(res, code, body, headers) {
  res.writeHead(code, Object.assign({ 'Content-Type': 'application/json' }, headers || {}));
  res.end(typeof body === 'string' ? body : JSON.stringify(body));
}

http.createServer((req, res) => {
  // ---- 控制接口（不计入 arrivals）----
  if (req.url.startsWith('/stats')) {
    const gaps = [];
    for (let i = 1; i < arrivals.length; i++) gaps.push(arrivals[i] - arrivals[i - 1]);
    return send(res, 200, { arrivals_ms: arrivals, gaps_ms: gaps, hits });
  }
  if (req.url.startsWith('/reset')) {
    arrivals = []; hits = 0; failStatus = 0; failTimes = 0; retryAfter = 0;
    return send(res, 200, { ok: true });
  }
  if (req.url.startsWith('/fail')) {
    let body = '';
    req.on('data', (c) => (body += c));
    req.on('end', () => {
      let p = {};
      try { p = JSON.parse(body); } catch (e) {}
      failStatus = Number(p.status || 503);
      failTimes = Number(p.times != null ? p.times : 1);
      retryAfter = Number(p.retry_after || 0);
      arrivals = []; hits = 0;
      send(res, 200, { ok: true, failStatus, failTimes, retryAfter });
    });
    return;
  }

  // ---- 真正的上游请求：先记录到达时刻 ----
  let body = '';
  req.on('data', (c) => (body += c));
  req.on('end', () => {
    arrivals.push(Date.now());
    hits++;

    // 前 failTimes 次返回指定错误
    if (failStatus && hits <= failTimes) {
      const headers = {};
      if (retryAfter > 0) headers['Retry-After'] = String(retryAfter);
      return send(res, failStatus, {
        error: { message: 'flaky upstream injected failure #' + hits, type: 'injected' },
      }, headers);
    }

    let p = {};
    try { p = JSON.parse(body); } catch (e) {}
    const usage = { prompt_tokens: 10, completion_tokens: 5, total_tokens: 15 };
    if (p.stream) {
      res.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache' });
      const chunk = {
        id: 'flaky', object: 'chat.completion.chunk', created: 0, model: p.model,
        choices: [{ index: 0, delta: { content: 'ok' }, finish_reason: 'stop' }],
        usage,
      };
      res.write('data: ' + JSON.stringify(chunk) + '\n\n');
      res.write('data: [DONE]\n\n');
      return res.end();
    }
    send(res, 200, {
      id: 'flaky', object: 'chat.completion', created: 0, model: p.model,
      choices: [{ index: 0, message: { role: 'assistant', content: 'ok' }, finish_reason: 'stop' }],
      usage,
    });
  });
}).listen(PORT, '0.0.0.0', () => console.log('flaky upstream on ' + PORT));
