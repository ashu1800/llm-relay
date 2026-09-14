// 记住界面上的筛选选择。
//
// 为什么要持久化：渠道一多，「只看某个分组」就是常态视角 —— 每次刷新网页、
// 或者从别的页面切回来都要重新选一遍，是纯粹的重复劳动。
//
// 这里刻意不引入 pinia：要存的只是「一个 key 对应一个字符串」，
// 用不着 store 那一套。与 theme store 里的 localStorage 用法保持同一口径
// （同一份数据只由它自己读写，键名带 llm-relay- 前缀避免和别的东西撞）。
//
// 读的时候必须容错：localStorage 里可能是上一次版本留下的旧值、也可能是
// 用户手改的脏数据。拿到不认识的值不能直接塞给界面 —— 那会得到一个
// 空列表加一个选不中的下拉框，看起来像「渠道全没了」。

const PREFIX = 'llm-relay-'

/** 读取一个已持久化的选择；值不在允许集合里时返回 fallback。 */
export function readStoredChoice(key: string, allowed: string[], fallback: string): string {
  try {
    const raw = localStorage.getItem(PREFIX + key)
    if (raw === null) return fallback
    return allowed.includes(raw) ? raw : fallback
  } catch {
    // 隐私模式下 localStorage 读写会抛异常，此时退化成「不记住」即可，
    // 不该因为一个筛选框让整个页面打不开
    return fallback
  }
}

/** 写入一个选择。写失败只影响「下次还记不记得」，不影响本次操作。 */
export function writeStoredChoice(key: string, value: string): void {
  try {
    localStorage.setItem(PREFIX + key, value)
  } catch {
    /* 忽略：见 readStoredChoice 的说明 */
  }
}
