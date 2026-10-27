package consoleauth

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"
)

// RFC 6238 的官方测试密钥（"12345678901234567890" 的 ASCII），
// 8 位档的官方向量在这里换算成 6 位（% 10^6）后自洽。
// SHA-1 档的 8 位参考值（T=59s 等）来自 RFC 附录 B。
const rfcSecretB32 = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

// t0 是 RFC 6238 附录 B 使用的参考时刻 T0。
var t0 = time.Unix(59, 0).UTC()

func TestCodeMatchesRFC6238Vectors(t *testing.T) {
	// RFC 6238 附录 B（SHA-1，8 位）→ 取后 6 位即 6 位档期望值。
	// 时间与 8 位期望值成对给出；6 位值 = 8 位值 mod 10^6。
	cases := []struct {
		at    time.Time
		eight string
	}{
		{time.Unix(59, 0).UTC(), "94287082"},
		{time.Unix(1111111109, 0).UTC(), "07081804"},
		{time.Unix(1111111111, 0).UTC(), "14050471"},
		{time.Unix(1234567890, 0).UTC(), "89005924"},
		{time.Unix(2000000000, 0).UTC(), "69279037"},
		{time.Unix(20000000000, 0).UTC(), "65353130"},
	}
	for _, c := range cases {
		got, err := Code(rfcSecretB32, c.at)
		if err != nil {
			t.Fatalf("Code(%d) 出错: %v", c.at.Unix(), err)
		}
		want := c.eight[len(c.eight)-6:]
		if got != want {
			t.Errorf("T=%d 动态码 = %s, 期望 %s（8 位官方向量 %s 取模）", c.at.Unix(), got, want, c.eight)
		}
	}
}

func TestVerifyAcceptsWindowAndRejectsOutside(t *testing.T) {
	code, err := Code(rfcSecretB32, t0)
	if err != nil {
		t.Fatal(err)
	}
	// 同一时刻：过
	if !Verify(rfcSecretB32, code, t0) {
		t.Fatal("当前窗口的码应通过")
	}
	// 前一个窗口的码在 +1 窗口宽容内：过
	prev, _ := Code(rfcSecretB32, t0.Add(-totpStep))
	if !Verify(rfcSecretB32, prev, t0) {
		t.Fatal("前一窗口的码应通过（±1 窗口宽容）")
	}
	// 超出窗口（-2 步长）：拒
	far, _ := Code(rfcSecretB32, t0.Add(-2*totpStep))
	if Verify(rfcSecretB32, far, t0) {
		t.Fatal("超出 ±1 窗口的码应被拒绝")
	}
	// 形态不对：拒
	for _, bad := range []string{"", "12345", "1234567", "12a456", "abcdef"} {
		if Verify(rfcSecretB32, bad, t0) {
			t.Errorf("非法码 %q 应被拒绝", bad)
		}
	}
}

func TestGenerateSecretLengthAndEncoding(t *testing.T) {
	s, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	// 20 字节 → Base32 无填充 32 字符
	if len(s) != 32 {
		t.Fatalf("密钥长度 = %d, 期望 32", len(s))
	}
	if strings.ContainsAny(s, "0189") {
		t.Fatalf("Base32 字母表不含 0/1/8/9，得到 %s", s)
	}
	if _, err := Code(s, t0); err != nil {
		t.Fatalf("生成的密钥应能直接计算动态码: %v", err)
	}
}

func TestProvisioningURIEscapesAndCarriesParams(t *testing.T) {
	uri := ProvisioningURI("ABC234DEF", "My: Relay App", "a b")
	// label 的 issuer 部分必须转义冒号，否则认证器会把 label 切错
	if !strings.HasPrefix(uri, "otpauth://totp/My%3A%20Relay%20App:a%20b?") {
		t.Fatalf("label 转义错误: %s", uri)
	}
	for _, frag := range []string{"secret=ABC234DEF", "issuer=My%3A%20Relay%20App", "algorithm=SHA1", "digits=6", "period=30"} {
		if !strings.Contains(uri, frag) {
			t.Errorf("URI 缺少 %s: %s", frag, uri)
		}
	}
}

func TestSealerRoundtripAndWrongKey(t *testing.T) {
	s1, err := NewSealer("master-secret-1")
	if err != nil {
		t.Fatal(err)
	}
	enc, err := s1.Seal("JBSWY3DPEHPK3PXP")
	if err != nil {
		t.Fatal(err)
	}
	if enc == "" || strings.Contains(enc, "JBSW") {
		t.Fatal("密文不应包含明文片段")
	}
	plain, err := s1.Open(enc)
	if err != nil || plain != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("解密回原失败: %q %v", plain, err)
	}
	if _, err := s1.Seal(""); err == nil {
		t.Fatal("空串应拒绝加密（空密文与未设置是两个语义）")
	}
	// 换一把主密钥解不开
	s2, _ := NewSealer("master-secret-2")
	if _, err := s2.Open(enc); err == nil {
		t.Fatal("错误主密钥应解密失败")
	}
	// 域分隔：与裸 SHA-256 派生的渠道密钥不同轴
	if enc2, _ := s1.Seal("x"); enc2 == "" {
		t.Fatal("不可达")
	}
}

func TestNewCredVersionShape(t *testing.T) {
	v1, err := NewCredVersion()
	if err != nil {
		t.Fatal(err)
	}
	v2, _ := NewCredVersion()
	if len(v1) != 16 {
		t.Fatalf("版本应为 8 字节 hex（16 字符），得到 %d", len(v1))
	}
	if _, err := hex.DecodeString(v1); err != nil {
		t.Fatalf("版本应为合法 hex: %v", err)
	}
	if v1 == v2 {
		t.Fatal("两次生成的版本不应相同")
	}
}

// ---- Store 状态机 ----

func newTestStore(t *testing.T) (*Store, func() string) {
	t.Helper()
	sealer, err := NewSealer("test-master")
	if err != nil {
		t.Fatal(err)
	}
	s := NewMemoryStore(sealer)
	return s, func() string { return s.Snapshot().CredVersion }
}

func TestStoreSetupKeyThenMatchAndChange(t *testing.T) {
	s, ver := newTestStore(t)
	if s.Snapshot().Method != MethodUnset {
		t.Fatal("初始应为 unset")
	}
	if s.MatchKey("anything") {
		t.Fatal("unset 状态不应匹配任何密钥")
	}
	if err := s.SetupKey("0123456789abcdef-key"); err != nil {
		t.Fatal(err)
	}
	v1 := ver()
	if !s.MatchKey("0123456789abcdef-key") || s.MatchKey("wrong") {
		t.Fatal("密钥匹配不正确")
	}
	if s.Snapshot().KeyHash == "" {
		t.Fatal("key_hash 不应为空")
	}
	// 改密：旧密钥错 → 不变
	if err := s.ChangeKey("wrong", "new-key-0123456789"); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("旧密钥错误应返回 ErrWrongKey，得到 %v", err)
	}
	if !s.MatchKey("0123456789abcdef-key") {
		t.Fatal("失败的改密不应变更密钥")
	}
	// 改密：成功 → 新密钥生效、旧密钥失效、版本轮换
	if err := s.ChangeKey("0123456789abcdef-key", "new-key-0123456789"); err != nil {
		t.Fatal(err)
	}
	if s.MatchKey("0123456789abcdef-key") || !s.MatchKey("new-key-0123456789") {
		t.Fatal("改密后新旧密钥状态不正确")
	}
	if ver() == v1 {
		t.Fatal("改密必须轮换会话版本")
	}
}

func totpFlow(t *testing.T, s *Store, confirm func(string, time.Time) error, now time.Time) {
	t.Helper()
	_, _, err := s.SetupTOTPInit()
	if err != nil {
		t.Fatal(err)
	}
	secret := s.Snapshot().Pending.SecretEnc
	sealer := s.sealer
	plain, err := sealer.Open(secret)
	if err != nil {
		t.Fatal(err)
	}
	code, _ := Code(plain, now)
	if err := confirm(code, now); err != nil {
		t.Fatalf("正确动态码确认失败: %v", err)
	}
}

func TestStoreSetupTOTPActivatesAndRemovesKey(t *testing.T) {
	s, ver := newTestStore(t)
	_ = s.SetupKey("0123456789abcdef-key")
	v1 := ver()
	now := time.Unix(1_800_000_000, 0).UTC()
	totpFlow(t, s, s.SetupTOTPConfirm, now)
	st := s.Snapshot()
	if st.Method != MethodTOTP || st.TOTPSecretEnc == "" || st.KeyHash != "" || st.Pending != nil {
		t.Fatalf("绑定后的状态不正确: %+v", st)
	}
	if ver() == v1 {
		t.Fatal("绑定 2FA 必须轮换会话版本")
	}
	// 旧密钥不再能登录
	if s.MatchKey("0123456789abcdef-key") {
		t.Fatal("切到 2FA 后密钥登录应失效（二选一）")
	}
}

func TestStoreReplayRejected(t *testing.T) {
	s, _ := newTestStore(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	totpFlow(t, s, s.SetupTOTPConfirm, now)
	// 登录用掉一个码。绑定确认时已消费 now 窗口的码（防重放按
	// 「密钥+码」记账），所以登录用下一个窗口的码 —— 同码重放
	// 本来就该被拒，下一段单独测。
	secretPlain := s.totpPlainForTest(t)
	at := now.Add(totpStep)
	code, _ := Code(secretPlain, at)
	if err := s.VerifyTOTP(code, at); err != nil {
		t.Fatalf("首次登录应通过: %v", err)
	}
	err := s.VerifyTOTP(code, at)
	if !errors.Is(err, ErrReplayed) {
		t.Fatalf("同码重放应返回 ErrReplayed，得到 %v", err)
	}
	// 再下一个窗口的新码可用（登录已消费 +1 窗口的码）
	next, _ := Code(secretPlain, at.Add(totpStep))
	if err := s.VerifyTOTP(next, at.Add(totpStep)); err != nil {
		t.Fatalf("新窗口的码应可用: %v", err)
	}
}

func TestStoreSwitchBetweenMethods(t *testing.T) {
	s, ver := newTestStore(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	// key → totp（已登录切换，init 免验证）
	_ = s.SetupKey("0123456789abcdef-key")
	if _, _, err := s.SetupTOTPInit(); err != nil {
		t.Fatal(err)
	}
	plain, _ := s.sealer.Open(s.Snapshot().Pending.SecretEnc)
	code, _ := Code(plain, now)
	if err := s.SwitchToTOTPConfirm(code, now); err != nil {
		t.Fatal(err)
	}
	if s.Snapshot().Method != MethodTOTP {
		t.Fatal("应已切到 totp")
	}
	// totp → key：必须验当前码
	secretPlain := s.totpPlainForTest(t)
	good, _ := Code(secretPlain, now.Add(totpStep))
	if err := s.SwitchToKey("000000", "new-key-0123456789", now.Add(totpStep)); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("错误动态码应拒绝切换: %v", err)
	}
	if err := s.SwitchToKey(good, "new-key-0123456789", now.Add(totpStep)); err != nil {
		t.Fatal(err)
	}
	st := s.Snapshot()
	if st.Method != MethodKey || st.KeyHash == "" || st.TOTPSecretEnc != "" {
		t.Fatalf("切回密钥后的状态不正确: %+v", st)
	}
	if !s.MatchKey("new-key-0123456789") {
		t.Fatal("切换设置的新密钥应立即生效")
	}
	_ = ver()
}

func TestStoreRebindTOTP(t *testing.T) {
	s, ver := newTestStore(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	totpFlow(t, s, s.SetupTOTPConfirm, now)
	v1 := ver()
	oldPlain := s.totpPlainForTest(t)
	// 换绑 init：错误码拒绝
	bad, _ := Code(oldPlain, now.Add(totpStep))
	_ = bad
	if _, _, err := s.RebindTOTPInit("999999", now.Add(totpStep)); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("换绑 init 应验当前码: %v", err)
	}
	good, _ := Code(oldPlain, now.Add(totpStep))
	if _, _, err := s.RebindTOTPInit(good, now.Add(totpStep)); err != nil {
		t.Fatal(err)
	}
	if s.Snapshot().Pending == nil {
		t.Fatal("换绑 init 后应有 pending")
	}
	newPlain, err := s.sealer.Open(s.Snapshot().Pending.SecretEnc)
	if err != nil {
		t.Fatal(err)
	}
	if newPlain == oldPlain {
		t.Fatal("换绑应生成新密钥")
	}
	// 确认前旧密钥仍有效（绑定未切换）
	oldCode, _ := Code(oldPlain, now.Add(2*totpStep))
	if err := s.VerifyTOTP(oldCode, now.Add(2*totpStep)); err != nil {
		t.Fatalf("确认前旧密钥应继续有效: %v", err)
	}
	// 确认新码 → 换绑完成
	newCode, _ := Code(newPlain, now.Add(2*totpStep))
	if err := s.RebindTOTPConfirm(newCode, now.Add(2*totpStep)); err != nil {
		t.Fatal(err)
	}
	if s.Snapshot().Pending != nil {
		t.Fatal("换绑完成后 pending 应清空")
	}
	if ver() == v1 {
		t.Fatal("换绑必须轮换会话版本")
	}
}

func TestStorePendingOverwrite(t *testing.T) {
	s, _ := newTestStore(t)
	// 两次 init：pending 被覆盖，只有最后一份可用
	_, _, _ = s.SetupTOTPInit()
	_, uri2, _ := s.SetupTOTPInit()
	if !strings.Contains(uri2, "otpauth://") {
		t.Fatal("URI 应有效")
	}
	plain, _ := s.sealer.Open(s.Snapshot().Pending.SecretEnc)
	now := time.Unix(1_800_000_000, 0).UTC()
	code, _ := Code(plain, now)
	if err := s.SetupTOTPConfirm(code, now); err != nil {
		t.Fatalf("第二次 init 的码应可确认: %v", err)
	}
}

func TestStoreConfirmWithoutInit(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.SetupTOTPConfirm("123456", time.Now()); !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("无 pending 时确认应返回 ErrNotInitialized，得到 %v", err)
	}
}

func TestStoreSetupKeyClearsTOTP(t *testing.T) {
	s, _ := newTestStore(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	totpFlow(t, s, s.SetupTOTPConfirm, now)
	// 直接重设密钥（向导里的另一条路径）应清掉 2FA
	if err := s.SetupKey("brand-new-key-0123456789"); err != nil {
		t.Fatal(err)
	}
	st := s.Snapshot()
	if st.Method != MethodKey || st.TOTPSecretEnc != "" {
		t.Fatalf("重设密钥应清掉 2FA: %+v", st)
	}
}

// totpPlainForTest 解出当前绑定的 TOTP 明文密钥（仅测试用）。
func (s *Store) totpPlainForTest(t *testing.T) string {
	t.Helper()
	enc := s.Snapshot().TOTPSecretEnc
	if enc == "" {
		t.Fatal("当前没有绑定的 TOTP 密钥")
	}
	plain, err := s.sealer.Open(enc)
	if err != nil {
		t.Fatal(err)
	}
	return plain
}
