// Package consoleauth 管理台认证的纯逻辑与状态存取。
//
// 为什么要独立一个包：认证方式（TOTP 二步验证 / 管理密钥，二选一）是
// 新增的，它的持久化（settings 表里一行 JSON）、TOTP 算法与防重放、
// 以及「凭据变更必须轮换会话版本」这条不变式，都不属于 HTTP 层。
// api 包负责路由与会话 Cookie，这个包负责回答「这个码/这把密钥对不对」
// 与「当前是什么认证方式」。两层各测各的。
//
// 认证状态是 settings 表里的一行（key=StateKey），不是一张新表：
// 全站只有一个管理员，行级状态用键值表是同一抽象层级（update 包的
// 配置、store 的迁移标记都这么做），没有第二个查询维度值得建表。
// 这也意味着不改 model 实体、不碰 AutoMigrate —— 存量库升级零迁移。
package consoleauth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

// StateKey 是认证状态在 settings 表里的主键。
// 带 _v1 后缀：将来状态结构不兼容地变化时换 _v2，读取端按前缀分派。
const StateKey = "console_auth_v1"

// TOTP 绑定二维码里的发行方与账号。单管理员产品，两者都是常量：
// 认证器 App 里显示为「LLM Relay: admin」。
const (
	TOTPIssuer  = "LLM Relay"
	TOTPAccount = "admin"
)

// Method 是当前的认证方式。
type Method string

const (
	// MethodUnset 表示从未完成过初始化：登录只能用引导密钥
	// （环境变量 RELAY_ADMIN_KEY），且登录页要先走设置向导。
	// 存量部署升级后也处于这个状态 —— 与旧行为完全一致。
	MethodUnset Method = "unset"
	// MethodTOTP 登录只凭 6 位动态码（RFC 6238，30 秒步长，SHA-1）。
	MethodTOTP Method = "totp"
	// MethodKey 登录凭用户在界面里手动设置的管理密钥。
	MethodKey Method = "key"
)

// PendingSecret 是一次绑定/换绑/切换过程中、尚未确认的临时 TOTP 密钥。
// 与 TOTPSecretEnc 分开存：确认前当前密钥保持不变，扫码失败随时可以
// 重新生成 pending（并发重复 init 以最后一次为准 —— 单管理员场景下
// 「最后点开的那个二维码」就是用户正在扫的那个）。
type PendingSecret struct {
	SecretEnc string    `json:"secret_enc"` // AES-GCM 密文
	CreatedAt time.Time `json:"created_at"`
}

// State 是 settings 表里那一行 JSON 的形状。
type State struct {
	Method Method `json:"method"`
	// KeyHash 是 method=key 时的登录密钥 SHA-256 十六进制摘要，
	// 与引导密钥同一哈希口径。method=totp 时必须为空串 ——
	// 切到 2FA 就删密钥，二选一是硬约束，留着旧哈希只会让人误以为
	// 密钥登录还活着。
	KeyHash string `json:"key_hash,omitempty"`
	// TOTPSecretEnc 是 method=totp 时的 Base32 密钥密文（AES-GCM）。
	// 不落明文：settings 表会进数据库备份，渠道密钥都是密文，
	// 2FA 密钥没有资格更宽。
	TOTPSecretEnc string `json:"totp_secret_enc,omitempty"`
	// CredVersion 是会话令牌的版本（8 随机字节 hex）。会话令牌把它
	// 签进载荷，任何凭据变更（设密钥/绑 2FA/改密/换绑/切换）都必须
	// 重新生成 —— 这是无状态令牌唯一的吊销手段，改凭据不改版本等于
	// 复制过令牌的人在你换密码后还能继续用。
	CredVersion string `json:"cred_version"`
	// Pending 是进行中的绑定；nil 表示没有。
	Pending *PendingSecret `json:"pending,omitempty"`
}

func (s *State) clone() *State {
	if s == nil {
		return nil
	}
	cp := *s
	if s.Pending != nil {
		p := *s.Pending
		cp.Pending = &p
	}
	return &cp
}

// 错误只有「业务上可判定」的这几类；带上下文的包装错误由各方法生成，
// api 层按 errors.Is 分派成中文提示。
var (
	// ErrNotInitialized：没有待确认的绑定（页面刷新丢了 pending、
	// 或直接调接口跳过了 init）。
	ErrNotInitialized = errors.New("没有进行中的绑定，请先重新生成二维码")
	// ErrWrongMethod：当前认证方式与操作不匹配（如密钥模式下换绑 2FA）。
	ErrWrongMethod = errors.New("当前认证方式不支持该操作")
	// ErrInvalidCode：动态码错误（含已过期、非数字、长度不对）。
	ErrInvalidCode = errors.New("动态码不正确")
	// ErrReplayed：动态码已被使用（防重放命中）。文案面向人：
	// 它会被原样展示给用户，指引是「等下一个窗口」。
	ErrReplayed = errors.New("动态码已被使用，请等待下一个 30 秒窗口的新码")
	// ErrWrongKey：密钥不匹配（登录/改密的旧密钥校验）。
	ErrWrongKey = errors.New("密钥不正确")
)

// NewState 返回未初始化的空白状态。
//
// CredVersion 在这里就生成（而不是等第一次凭据变更）：unset 模式下
// 引导密钥登录也要签会话，会话必须绑定版本才有吊销能力 —— 否则
// 「完成向导」轮换的是一个原本不存在的版本，旧引导会话照样活着。
// crypto/rand 失败时版本为空，api 层对空版本 fail closed（拒签/拒验），
// 这是可接受的退化：rand 失败本身说明运行环境已不可信。
func NewState() *State {
	st := &State{Method: MethodUnset}
	if v, err := NewCredVersion(); err == nil {
		st.CredVersion = v
	}
	return st
}

// ---- 存取（gorm）----

// Load 从 settings 表读状态。没有任何记录、或值里缺 method 字段
// （手改库）时返回 MethodUnset 的空白状态 —— 存量部署升级后第一次
// 读取走的就是这条路，表现必须与「还没设置过」一致。
// 数据库错误原样返回：登录路径必须 fail closed。
func Load(db *gorm.DB) (*State, error) {
	st := NewState()
	if db == nil {
		return st, nil
	}
	// 扫成 string 而不是 []byte：postgres 驱动的 jsonb → []byte 会走
	// 「逐元素转换」的路径并报 `converting driver.Value type []uint8 ...
	// to a uint8`（实测踩过：写进去成功、重启后读不回来，认证状态
	// 静默退回 unset）。string 是这个驱动上稳定可用的扫描目标。
	var raw string
	err := db.Raw(`SELECT value::text FROM settings WHERE key = ?`, StateKey).Scan(&raw).Error
	if err != nil {
		return nil, fmt.Errorf("读取认证状态失败: %w", err)
	}
	if strings.TrimSpace(raw) == "" {
		return st, nil
	}
	if err := json.Unmarshal([]byte(raw), st); err != nil {
		// 解不开的行不能装作没设置（那会让守卫放行）：
		// 返回错误，登录路径 fail closed 并把原因讲清楚。
		return nil, fmt.Errorf("认证状态损坏（settings.%s 无法解析）: %w", StateKey, err)
	}
	if st.Method == "" {
		st.Method = MethodUnset
	}
	return st, nil
}

// Save 把状态整行写回 settings（upsert）。
// UPDATE 的 where 里带上旧值不是必须 —— 单管理员、串行操作，
// last-write-wins 是可接受的并发语义。
func Save(db *gorm.DB, st *State) error {
	if db == nil {
		return nil
	}
	if st == nil {
		return errors.New("认证状态为空，拒绝写入")
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("序列化认证状态失败: %w", err)
	}
	return db.Exec(`INSERT INTO settings (key, value, updated_at)
		VALUES (?, ?::jsonb, now())
		ON CONFLICT (key) DO UPDATE SET value = ?::jsonb, updated_at = now()`,
		StateKey, string(raw), string(raw)).Error
}

// ---- TOTP（RFC 6238，SHA-1 / 6 位 / 30 秒，与 Google Authenticator 等默认档一致）----

// 实现钉死的参数档。注释是给人看的，实现是给审的：
// 认证算法的档位一旦上线就不能「顺手」改 —— 改了所有已绑定设备全部失效。
const (
	totpStep    = 30 * time.Second
	totpDigits  = 6
	totpWindow  = 1  // 允许前后各 1 个步长（时钟偏移宽容，±30s）
	totpSecretN = 20 // 160 位，RFC 4226 建议的最小值；Base32 后 32 字符
)

// GenerateSecret 生成新的 TOTP 密钥，返回 Base32（无填充，可直接录入
// 认证器 App 的手输模式）。
func GenerateSecret() (string, error) {
	raw := make([]byte, totpSecretN)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", fmt.Errorf("生成 TOTP 密钥失败: %w", err)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw), nil
}

// b32Decode 容错解码 Base32：不同认证器对手输密钥的大小写、空格、
// 填充的处理并不一致，比对前统一掉。
func b32Decode(s string) ([]byte, error) {
	s = strings.ToUpper(strings.ReplaceAll(s, " ", ""))
	if pad := len(s) % 8; pad != 0 {
		s += strings.Repeat("=", 8-pad)
	}
	return base32.StdEncoding.DecodeString(s)
}

// counterAt 把时刻换成 RFC 4226 计数器。
func counterAt(t time.Time) uint64 {
	return uint64(t.Unix()) / uint64(totpStep.Seconds())
}

// codeAt 计算密钥在计数器 c 上的动态码
// （10 字节大端计数器 → HMAC-SHA-1 → 动态截断 → 6 位十进制）。
func codeAt(raw []byte, c uint64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], c)
	mac := hmac.New(sha1.New, raw)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	// RFC 4226 动态截断：取最后一字节的低 4 位做偏移，再取 4 字节去符号位
	off := sum[len(sum)-1] & 0x0f
	bin := (uint32(sum[off])&0x7f)<<24 | uint32(sum[off+1])<<16 |
		uint32(sum[off+2])<<8 | uint32(sum[off+3])
	return fmt.Sprintf("%0*d", totpDigits, bin%pow10(totpDigits))
}

// Code 计算密钥在时刻 t 的动态码。
func Code(secretB32 string, t time.Time) (string, error) {
	raw, err := b32Decode(secretB32)
	if err != nil || len(raw) == 0 {
		return "", fmt.Errorf("TOTP 密钥不是合法的 Base32: %w", err)
	}
	return codeAt(raw, counterAt(t)), nil
}

// Verify 常数时间比对 secret 在 ±window 窗口内的动态码。
// 窗口的意义是时钟偏移：手机时间差 20 秒不该被拒之门外，
// 差 90 秒则该被拒 —— 宽容只到不伤害安全性的边界为止。
func Verify(secretB32, code string, now time.Time) bool {
	raw, err := b32Decode(secretB32)
	if err != nil || len(raw) == 0 {
		return false
	}
	if !codeShape(code) {
		return false
	}
	for d := -totpWindow; d <= totpWindow; d++ {
		expect := codeAt(raw, counterAt(now.Add(time.Duration(d)*totpStep)))
		if subtle.ConstantTimeCompare([]byte(expect), []byte(code)) == 1 {
			return true
		}
	}
	return false
}

// codeShape 是纯形态检查：6 位数字。长度不对/含非数字直接出局，
// 不进常数时间路径。
func codeShape(code string) bool {
	if len(code) != totpDigits {
		return false
	}
	for i := 0; i < len(code); i++ {
		if code[i] < '0' || code[i] > '9' {
			return false
		}
	}
	return true
}

// pow10 是 10**n 的朴素实现 —— math.Pow 是浮点，认证计数器要的是精确整数。
func pow10(n int) uint32 {
	v := uint32(1)
	for i := 0; i < n; i++ {
		v *= 10
	}
	return v
}

// ---- 防重放 ----

// replayCache 记录「刚用过的动态码」。TOTP 的窗口验证意味着同一个码在
// 30 秒内可能命中两次 —— 攻击者钓鱼拿到一个码后立刻重放，是这类认证
// 最现实的攻击面。按「密钥指纹+码+计数器」记账，命中即拒绝。
//
// 容量有界（1024）：单管理员的正常用量是每次登录一条；到顶后淘汰最旧
// 条目而不是拒绝新码 —— 缓存的作用是挡重放，不是当限流用。
type replayCache struct {
	mu       sync.Mutex
	entries  map[string]time.Time
	capacity int
}

func newReplayCache(capacity int) *replayCache {
	if capacity <= 0 {
		capacity = 1024
	}
	return &replayCache{entries: map[string]time.Time{}, capacity: capacity}
}

// key 把「哪把密钥的哪个码」编成一个键。密钥原文不进键：
// 先哈希再拼接，缓存被 dump（内存取证、pprof）也拿不到密钥。
func replayKey(secretB32, code string, counter uint64) string {
	sum := sha256.Sum256([]byte(secretB32 + "|" + code))
	return hex.EncodeToString(sum[:8]) + "|" + strconv.FormatUint(counter, 10)
}

// seen 报告该码是否已用过；没有则记下来并返回 false（= 可用）。
// 「检查即消费」是刻意的：验证通过后同一码再提交一律拒绝，
// 不依赖调用方记得来「标记已用」。
func (r *replayCache) seen(secretB32, code string, counter uint64, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := replayKey(secretB32, code, counter)
	if _, ok := r.entries[k]; ok {
		return true
	}
	if len(r.entries) >= r.capacity {
		r.evictOldestLocked()
	}
	r.entries[k] = now
	return false
}

func (r *replayCache) evictOldestLocked() {
	var oldestKey string
	var oldest time.Time
	first := true
	for k, t := range r.entries {
		if first || t.Before(oldest) {
			oldest, oldestKey, first = t, k, false
		}
	}
	delete(r.entries, oldestKey)
}

// ---- 加解密 ----

// Sealer 加密 TOTP 密钥。密钥派生带域分隔前缀（"llm-relay/console-totp|"），
// 让 2FA 密文与渠道密钥密文（裸 SHA-256(secret)）、会话签名密钥
// （"llm-relay/console-session|"）互不相同 —— 一把被逆推不影响另一把，
// 与 api 包会话密钥的派生策略是同一条设计规则。
type Sealer struct {
	aead cipher.AEAD
}

func NewSealer(masterSecret string) (*Sealer, error) {
	if strings.TrimSpace(masterSecret) == "" {
		return nil, errors.New("加密主密钥为空")
	}
	sum := sha256.Sum256([]byte("llm-relay/console-totp|" + masterSecret))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, fmt.Errorf("初始化 AES 失败: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("初始化 GCM 失败: %w", err)
	}
	return &Sealer{aead: aead}, nil
}

func (s *Sealer) Seal(plain string) (string, error) {
	if plain == "" {
		return "", errors.New("拒绝加密空串：空密文与「未设置」是两个语义")
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("生成随机数失败: %w", err)
	}
	sealed := s.aead.Seal(nonce, nonce, []byte(plain), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

func (s *Sealer) Open(encoded string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("密文解码失败: %w", err)
	}
	ns := s.aead.NonceSize()
	if len(raw) < ns {
		return "", errors.New("密文长度不足")
	}
	plain, err := s.aead.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", errors.New("解密失败：主密钥不匹配或密文被篡改")
	}
	return string(plain), nil
}

// ---- Provisioning URI（otpauth://）----

// ProvisioningURI 生成认证器 App 扫码用的 otpauth://totp/ URI。
// 参数按 Google Authenticator 的解析口径：label 是「issuer:account」，
// issuer 同时放进 query（官方推荐的两处都给：老版本 App 只认 label
// 前缀，新版本优先读 issuer 参数）；label/issuer 里的冒号与空格
// 百分号转义，否则 App 会解析错位。
func ProvisioningURI(secretB32, issuer, account string) string {
	esc := func(s string) string {
		s = strings.ReplaceAll(s, ":", "%3A")
		s = strings.ReplaceAll(s, " ", "%20")
		return s
	}
	return "otpauth://totp/" + esc(issuer) + ":" + esc(account) +
		"?secret=" + secretB32 +
		"&issuer=" + esc(issuer) +
		"&algorithm=SHA1&digits=6&period=30"
}

// ---- 会话版本 ----

// NewCredVersion 生成新的会话版本（8 随机字节 hex）。
//
// 刻意不从凭据本身派生：密钥模式的版本曾是「密钥哈希前 8 字节」，
// 那意味着「哈希前 8 字节碰巧相同的另一把密钥」会保留旧会话有效性
// —— 概率 2^-64 可以忽略，但「版本=随机数」让这个论证根本不需要
// 发生：版本唯一的语义就是「变过没有」，那就让它纯粹随机。
func NewCredVersion() (string, error) {
	buf := make([]byte, 8)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", fmt.Errorf("生成会话版本失败: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// ---- Store：状态机变更 + 原子落库 ----

// Store 是状态机操作的入口：进程内缓存 + 互斥 + 每次变更整行落库。
//
// 缓存解决的是「每次导航都查库」：/auth/status 在前端每次路由守卫
// 都会打一次，读缓存把这条路径从 1 次 DB 往返降为 0；代价是「改库要
// 走本包的方法」—— 绕过 Store 直接 UPDATE settings 会造成缓存与库
// 漂移（恢复备份后重启进程即可对齐，恢复流程本来就要重启）。
//
// 变更的原子性：每个写操作先在状态副本上改，落库成功后才换掉内存
// 状态 —— 「内存改了、库里没写」的重启回退会把「以为已切到 2FA」
// 变成「密钥还活着」，那是安全事故，不是小毛病。
type Store struct {
	mu     sync.Mutex
	st     *State
	sealer *Sealer
	cache  *replayCache
	// save 为 nil 时不落库（内存模式）：单测用，也兜底 api 层拿不到
	// DB 的极端装配 —— 内存态在重启后消失，等效于回到未初始化。
	save func(*State) error
	// lastVerifyErr 是 verifyAndConsume 最近一次失败的原因，**只在持有
	// s.mu 时读写**。它是 ErrInvalidCode / ErrReplayed 两种之一
	// （ErrWrongMethod 在进 verifyAndConsume 之前就被各方法拦下了）。
	//
	// 为什么不直接返回 error：verifyAndConsume 还要回传命中的计数器
	// （防重放记账的落点，测试要断言它），再加一个 error 就是三返回值，
	// 而 Go 没有 Result 类型，「(counter uint64, err error)」里 err != nil
	// 时 counter 无意义的约定不如一个明确的 bool 好读。字段只在锁内
	// 存活，调用方全部是紧邻的 `if _, ok := ...; !ok { return s.lastVerifyErr }`，
	// 不存在跨调用点携带。
	//
	// NOTE(lead): 这是一个有意识的取舍，不是偷懒 —— 后续若重构，改成
	// 返回 (counter, err) 并删掉本字段也完全成立。
	lastVerifyErr error
}

// NewStore 打开状态存储：立即从库里加载一次。
// db 为 nil 时是纯内存模式（测试用）。
func NewStore(db *gorm.DB, sealer *Sealer) (*Store, error) {
	st, err := Load(db)
	if err != nil {
		return nil, err
	}
	s := &Store{st: st, sealer: sealer, cache: newReplayCache(1024)}
	if db != nil {
		s.save = func(next *State) error { return Save(db, next) }
	}
	return s, nil
}

// NewMemoryStore 构造内存模式的状态存储（测试与无库装配用）。
func NewMemoryStore(sealer *Sealer) *Store {
	return &Store{st: NewState(), sealer: sealer, cache: newReplayCache(1024)}
}

// Snapshot 返回当前状态的深拷贝：调用方可以安全地读，
// 不会看到后续变更的半成品。
func (s *Store) Snapshot() *State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.clone()
}

// commit 在锁内提交一次变更：副本已改好 → 落库 → 成功才换内存状态。
func (s *Store) commit(next *State) error {
	if s.save != nil {
		if err := s.save(next); err != nil {
			return fmt.Errorf("写入认证状态失败: %w", err)
		}
	}
	s.st = next
	return nil
}

// rotateVersion 生成新的会话版本。
func rotateVersion() (string, error) { return NewCredVersion() }

// SetupTOTPInit 生成 pending 密钥（覆盖旧的 pending），返回明文 Base32
// 与 otpauth URI。**不落库**：pending 只活在内存里，进程重启后重新点
// 一次「生成二维码」即可 —— 少一次写库，也避免「pending 写库失败但
// 二维码已经显示出去」这类半状态。当前密钥（若有）不受影响。
func (s *Store) SetupTOTPInit() (secretB32, uri string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	secret, err := GenerateSecret()
	if err != nil {
		return "", "", err
	}
	enc, err := s.sealer.Seal(secret)
	if err != nil {
		return "", "", err
	}
	next := s.st.clone()
	next.Pending = &PendingSecret{SecretEnc: enc, CreatedAt: time.Now()}
	// pending 不落库（见上），直接换内存态
	s.st = next
	return secret, ProvisioningURI(secret, TOTPIssuer, TOTPAccount), nil
}

// SetupTOTPConfirm 用 pending 密钥验证动态码；通过则切换 method=totp、
// 清 pending、轮换会话版本并落库。
func (s *Store) SetupTOTPConfirm(code string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st.Pending == nil {
		return ErrNotInitialized
	}
	secret, err := s.sealer.Open(s.st.Pending.SecretEnc)
	if err != nil {
		return fmt.Errorf("读取待绑定密钥失败: %w", err)
	}
	counter, ok := s.verifyAndConsume(secret, code, now)
	if !ok {
		return s.lastVerifyErr
	}
	v, err := rotateVersion()
	if err != nil {
		return err
	}
	next := s.st.clone()
	next.Method = MethodTOTP
	next.KeyHash = "" // 二选一：绑 2FA 即删密钥
	next.TOTPSecretEnc = s.st.Pending.SecretEnc
	next.Pending = nil
	next.CredVersion = v
	_ = counter
	return s.commit(next)
}

// SetupKey 设置密钥登录方式。强度校验在 api 层（要拼面向人的错误文案），
// 这里只做切换本身。
func (s *Store) SetupKey(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := rotateVersion()
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(key))
	next := s.st.clone()
	next.Method = MethodKey
	next.KeyHash = hex.EncodeToString(sum[:])
	next.TOTPSecretEnc = "" // 二选一：设密钥即删 2FA
	next.Pending = nil
	next.CredVersion = v
	return s.commit(next)
}

// SwitchToTOTPConfirm 已登录状态下切到 2FA：验证 pending 密钥的动态码
// 后提交。init 在登录态下做过、免二次验证 —— 持有有效会话本身就是凭证。
func (s *Store) SwitchToTOTPConfirm(code string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st.Pending == nil {
		return ErrNotInitialized
	}
	secret, err := s.sealer.Open(s.st.Pending.SecretEnc)
	if err != nil {
		return fmt.Errorf("读取待绑定密钥失败: %w", err)
	}
	if _, ok := s.verifyAndConsume(secret, code, now); !ok {
		return s.lastVerifyErr
	}
	v, err := rotateVersion()
	if err != nil {
		return err
	}
	next := s.st.clone()
	next.Method = MethodTOTP
	next.KeyHash = ""
	next.TOTPSecretEnc = s.st.Pending.SecretEnc
	next.Pending = nil
	next.CredVersion = v
	return s.commit(next)
}

// SwitchToKey 已登录状态下从 2FA 切到密钥：先验当前动态码
// （防「劫持了浏览器的人把主人的 2FA 静默换掉」），再设新密钥。
func (s *Store) SwitchToKey(currentCode, newKey string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st.Method != MethodTOTP || s.st.TOTPSecretEnc == "" {
		return ErrWrongMethod
	}
	secret, err := s.sealer.Open(s.st.TOTPSecretEnc)
	if err != nil {
		return fmt.Errorf("读取 TOTP 密钥失败: %w", err)
	}
	if _, ok := s.verifyAndConsume(secret, currentCode, now); !ok {
		return s.lastVerifyErr
	}
	v, err := rotateVersion()
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(newKey))
	next := s.st.clone()
	next.Method = MethodKey
	next.KeyHash = hex.EncodeToString(sum[:])
	next.TOTPSecretEnc = ""
	next.Pending = nil
	next.CredVersion = v
	return s.commit(next)
}

// ChangeKey 修改密钥登录方式的密钥。旧密钥错误时不做任何变更。
func (s *Store) ChangeKey(currentKey, newKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st.Method != MethodKey || s.st.KeyHash == "" {
		return ErrWrongMethod
	}
	sum := sha256.Sum256([]byte(currentKey))
	if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(s.st.KeyHash)) != 1 {
		return ErrWrongKey
	}
	v, err := rotateVersion()
	if err != nil {
		return err
	}
	newSum := sha256.Sum256([]byte(newKey))
	next := s.st.clone()
	next.KeyHash = hex.EncodeToString(newSum[:])
	next.CredVersion = v
	return s.commit(next)
}

// RebindTOTPInit 更换绑定：验证当前动态码后生成新的 pending。
// 与 SetupTOTPInit 的差别只在前置验证 —— 换绑必须先证明「是主人本人在
// 操作」，否则一个被 XSS 的页面就能静默换掉 2FA。
func (s *Store) RebindTOTPInit(currentCode string, now time.Time) (secretB32, uri string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st.Method != MethodTOTP || s.st.TOTPSecretEnc == "" {
		return "", "", ErrWrongMethod
	}
	cur, err := s.sealer.Open(s.st.TOTPSecretEnc)
	if err != nil {
		return "", "", fmt.Errorf("读取 TOTP 密钥失败: %w", err)
	}
	if _, ok := s.verifyAndConsume(cur, currentCode, now); !ok {
		return "", "", s.lastVerifyErr
	}
	secret, err := GenerateSecret()
	if err != nil {
		return "", "", err
	}
	enc, err := s.sealer.Seal(secret)
	if err != nil {
		return "", "", err
	}
	next := s.st.clone()
	next.Pending = &PendingSecret{SecretEnc: enc, CreatedAt: time.Now()}
	s.st = next // pending 不落库（同 SetupTOTPInit）
	return secret, ProvisioningURI(secret, TOTPIssuer, TOTPAccount), nil
}

// RebindTOTPConfirm 完成换绑：验证新密钥的动态码后提交。
func (s *Store) RebindTOTPConfirm(code string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st.Pending == nil {
		return ErrNotInitialized
	}
	secret, err := s.sealer.Open(s.st.Pending.SecretEnc)
	if err != nil {
		return fmt.Errorf("读取待绑定密钥失败: %w", err)
	}
	if _, ok := s.verifyAndConsume(secret, code, now); !ok {
		return s.lastVerifyErr
	}
	v, err := rotateVersion()
	if err != nil {
		return err
	}
	next := s.st.clone()
	next.TOTPSecretEnc = s.st.Pending.SecretEnc
	next.Pending = nil
	next.CredVersion = v
	return s.commit(next)
}

// verifyAndConsume 在锁内完成「窗口验证 + 防重放记账」。
// 返回命中的计数器（供调用方日志/测试断言）与是否通过；
// 失败原因写在 s.lastVerifyErr（见 struct 注释）。
func (s *Store) verifyAndConsume(secretB32, code string, now time.Time) (uint64, bool) {
	s.lastVerifyErr = ErrInvalidCode
	if !Verify(secretB32, code, now) {
		return 0, false
	}
	counter := counterAt(now)
	// 找到命中的那个窗口的计数器：同一个码在相邻窗口重复出现时
	// （例如 234567 在 t 与 t+30s 都在窗口内），记账要落在真实命中的
	// 那个窗口上，否则 30 秒内重放仍可能绕过。
	for d := -totpWindow; d <= totpWindow; d++ {
		t := now.Add(time.Duration(d) * totpStep)
		if expect, err := Code(secretB32, t); err == nil && expect == code {
			counter = counterAt(t)
			break
		}
	}
	if s.cache.seen(secretB32, code, counter, now) {
		s.lastVerifyErr = ErrReplayed
		return counter, false
	}
	return counter, true
}

// MatchKey 报告给定密钥是否等于当前登录密钥（method=key 时）。
// 常数时间比对哈希摘要。
func (s *Store) MatchKey(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st.Method != MethodKey || s.st.KeyHash == "" {
		return false
	}
	sum := sha256.Sum256([]byte(key))
	return subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(s.st.KeyHash)) == 1
}

// VerifyTOTP 验证当前绑定密钥的动态码（登录用）。带防重放。
func (s *Store) VerifyTOTP(code string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st.Method != MethodTOTP || s.st.TOTPSecretEnc == "" {
		return ErrWrongMethod
	}
	secret, err := s.sealer.Open(s.st.TOTPSecretEnc)
	if err != nil {
		return fmt.Errorf("读取 TOTP 密钥失败: %w", err)
	}
	if _, ok := s.verifyAndConsume(secret, code, now); !ok {
		return s.lastVerifyErr
	}
	return nil
}

// ---- 仅测试用的解密辅助（exported 是为了跨包测试：api 的接线测试要算码）----
// 不要在生产代码里调用：明文密钥只在测试里合理存在。

// PendingPlainForTest 解出 pending 密钥的明文 Base32。
func (s *Store) PendingPlainForTest() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st.Pending == nil {
		return "", ErrNotInitialized
	}
	return s.sealer.Open(s.st.Pending.SecretEnc)
}

// TOTPSecretPlainForTest 解出当前绑定密钥的明文 Base32。
func (s *Store) TOTPSecretPlainForTest() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st.TOTPSecretEnc == "" {
		return "", ErrWrongMethod
	}
	return s.sealer.Open(s.st.TOTPSecretEnc)
}
