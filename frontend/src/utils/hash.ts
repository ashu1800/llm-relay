// 字符串散列：把名字映射成一个稳定的 32 位无符号整数。
//
// 模型胶囊与分组胶囊都靠它取色相，所以放在一处 —— 两个地方各写一份，
// 改算法时必然漏掉一个，表现是「同一类标签颜色规律不一致」。
//
// FNV-1a 打底，再过一遍 murmur3 的 fmix32 收尾。
//
// 两层都不能省：日志里的模型名高度相似（no-such-model-tz /
// no-such-model-filter / slow-concurrency-test 这类探针名字只差几个字符），
// 「h = h*31 + c」那种弱散列会让它们全撞到同一个色相；
// 而 FNV 单独用也不够 —— 它的低位对短串混合不足，取模前不过 fmix
// 同样会出现成对的同色。
export function hash32(s: string): number {
  let h = 0x811c9dc5
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i)
    h = Math.imul(h, 0x01000193)
  }
  h ^= h >>> 16
  h = Math.imul(h, 0x85ebca6b)
  h ^= h >>> 13
  h = Math.imul(h, 0xc2b2ae35)
  h ^= h >>> 16
  return h >>> 0
}
