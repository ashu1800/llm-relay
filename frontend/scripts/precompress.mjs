// 为前端产物生成同目录的 .gz 兄弟文件（构建期预压缩）。
//
// 为什么挂在这里、而不是让 Go 在请求路径上现压：
//
//   · 产物是通过 `//go:embed all:dist` 在**编译期**烙进二进制的，所以压缩
//     必须在产物就位之前完成。而「产物怎么来」这件事在仓库里有三条路
//     （deploy/Dockerfile 的前端阶段、release.yml 的构建步骤、
//      verify-release-artifacts.sh 的本地副本），它们唯一的共同点是
//      **都执行 `npm run build`**。挂在这一步，三条路自动全覆盖；
//     挂在任何一条下游，另外两条就会安静地发不压缩的资源。
//   · 压缩级别可以开到最高，代价只是构建慢一点；现压则要每次请求都花这个
//     CPU，而静态资源会被反复请求。
//   · 运行时只做「选文件 + 发字节」，没有压到一半失败这种状态。
//
// 输出必须**可复现**：不写 gzip 头里的文件名与时间戳，同一份输入每次压出
// 完全相同的字节。否则每次构建产物都不同，镜像层缓存与「内容是否变化」
// 的判断都会失去意义。
//
// 幂等：重复执行覆盖已有的 .gz；源文件本身绝不改动。
import { gzipSync, constants as zlibConstants } from 'node:zlib'
import { readdirSync, readFileSync, statSync, writeFileSync, rmSync, renameSync } from 'node:fs'
import { join, extname, relative } from 'node:path'

// 只压这些扩展名。
//
// 已经压缩过的格式（png/jpg/webp/woff2/ico）再压一遍通常**更大**，
// 而且白白多花构建时间 —— 它们本来就压不动。
// .map 也压：sourcemap 是纯文本，压缩比很高。
// 字体里 .woff2 是 brotli 压缩的（压不动，排除）；.ttf/.otf 未压缩，
// 收益明显（实测 312 KB → 103 KB）。
const COMPRESSIBLE = new Set([
  '.html', '.js', '.mjs', '.css', '.json', '.map', '.svg', '.txt', '.webmanifest',
  '.ttf', '.otf', '.woff'
])

// 小于这个体积的文件不压：几百字节的文件压完常因 gzip 头尾而**更大**，
// 为它多存一份文件并不划算。
const MIN_SIZE = 1024

/**
 * 递归收集目录下所有可压缩文件。
 * @param {string} dir
 * @returns {string[]} 文件绝对路径
 */
function collect(dir) {
  const out = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, entry.name)
    if (entry.isDirectory()) {
      out.push(...collect(p))
    } else if (entry.isFile() && COMPRESSIBLE.has(extname(entry.name).toLowerCase())) {
      out.push(p)
    }
  }
  return out
}

/**
 * 压缩一个文件，返回 { raw, gz } 字节数；压不动则返回 null（并清掉 .gz）。
 * @param {string} file
 */
function compressOne(file) {
  const raw = readFileSync(file)
  if (raw.length < MIN_SIZE) return null

  // level 9（最高）—— 构建期只做一次，运行时不再付这个代价。
  // mtime 固定为 0：gzip 头里带真实时间会让每次构建的产物都不同。
  const gz = gzipSync(raw, { level: zlibConstants.Z_BEST_COMPRESSION, mtime: 0 })

  // 压完反而更大就别留 —— 留着只会让运行时多发一份没用的文件。
  // 正常情况下不会发生（上面的扩展名表已排除压不动的格式），
  // 但规则交给数据判断更稳。
  if (gz.length >= raw.length) {
    rmSync(file + '.gz', { force: true })
    return null
  }

  // 先写临时文件再改名：中途失败不会留下半截的 .gz。
  // 服务端是按「.gz 是否存在」决定发不发的，半截文件会被当成有效产物发出去。
  const tmp = file + '.gz.tmp'
  writeFileSync(tmp, gz)
  renameSync(tmp, file + '.gz')
  return { raw: raw.length, gz: gz.length }
}

function main() {
  const dist = process.argv[2] || 'dist'

  let stat
  try {
    stat = statSync(dist)
  } catch {
    console.error(`预压缩失败：目录不存在 ${dist}`)
    process.exit(1)
  }
  if (!stat.isDirectory()) {
    console.error(`预压缩失败：${dist} 不是目录`)
    process.exit(1)
  }

  const files = collect(dist)
  let count = 0
  let skipped = 0
  let rawTotal = 0
  let gzTotal = 0

  for (const f of files) {
    const r = compressOne(f)
    if (!r) {
      skipped++
      continue
    }
    count++
    rawTotal += r.raw
    gzTotal += r.gz
  }

  const ratio = rawTotal > 0 ? ((gzTotal / rawTotal) * 100).toFixed(1) : '0.0'
  console.log(
    `预压缩完成：${count} 个文件  ${rawTotal} B → ${gzTotal} B（${ratio}%），` +
      `跳过 ${skipped} 个（过小或压不动）`
  )
  // 一个都没压到说明扩展名表或产物结构变了，这时候静默通过等于
  // 「压缩悄悄失效」，宁可让构建失败。
  if (files.length > 0 && count === 0) {
    console.error('预压缩失败：产物里有可压缩文件，但一个都没压成功 —— 请检查上面的规则')
    process.exit(1)
  }
}

main()
