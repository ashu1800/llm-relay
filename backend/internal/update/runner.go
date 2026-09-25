package update

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"
)

// updaterSocketPath 是宿主侧更新器的 unix socket 路径。
//
// 必须是**宿主机上的绝对路径**，且在容器里挂载到同一个路径 ——
// 容器内外的路径一致是这套机制能工作的前提（见 deploy/docker-compose.yml
// 的 /run/llm-relay-updater.sock 挂载，以及 install.sh 里 systemd 单元
// 的 RuntimeDirectory）。
//
// 用 /run 而不是 /tmp：/run 在多数发行版上是 tmpfs 且由 systemd 按服务
// 生命周期管理（RuntimeDirectory=llm-relay），重启后自动清理，
// 不会留下一个指向已死进程的陈旧 socket 让客户端误判「更新器还在」。
const updaterSocketPath = "/run/llm-relay-updater.sock"

// updaterSocketEnv 允许用环境变量覆盖 socket 路径。
// 主要给测试与非常规部署用（例如把 updater 跑在别的用户下、
// 或者用 /tmp 做联调）。
const updaterSocketEnv = "RELAY_UPDATER_SOCKET"

// SocketPath 返回当前使用的 socket 路径。
func SocketPath() string {
	if v := strings.TrimSpace(os.Getenv(updaterSocketEnv)); v != "" {
		return v
	}
	return updaterSocketPath
}

// socketRunner 通过 unix socket 与宿主侧更新器通信。
type socketRunner struct {
	path   string
	client *http.Client
}

// NewSocketRunner 构造宿主侧更新器的客户端。
//
// 这一步**不检查 socket 是否存在** —— 更新器可能比本服务晚启动
// （systemd 单元之间没有严格顺序），创建时就判定「不在场」会让
// 一次正常的重启顺序变成「更新功能永久不可用」。可用性在真正
// 发起请求时才知道（Upgrade 连不上会返回 ErrUpdaterUnavailable）。
func NewSocketRunner() UpgradeRunner {
	path := SocketPath()
	return &socketRunner{
		path: path,
		client: &http.Client{
			// 通过 unix socket 访问本机上的一个小服务，
			// 正常情况下是毫秒级。给 10 秒是因为「拉镜像」这类动作的
			// **启动**也可能要几秒（它要先做一次 docker 客户端探测）
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", path)
				},
				// socket 是本地连接，不需要连接池之外的任何配置；
				// 明确关掉 keep-alive 会让每次都重新拨号（便宜且避免陈旧连接）
				DisableKeepAlives: false,
			},
		},
	}
}

// Upgrade 请求更新器执行一次升级。
func (r *socketRunner) Upgrade(ctx context.Context) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	if err := r.post(ctx, "/upgrade", nil, &out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", errors.New("更新器没有返回升级 ID")
	}
	return out.ID, nil
}

// Progress 查询升级进度。
func (r *socketRunner) Progress(ctx context.Context, id string) (*UpgradeProgress, error) {
	if strings.TrimSpace(id) == "" {
		// 不传 ID 时取最近一次升级 —— 界面刷新后手上没有 ID，
		// 但仍想知道「刚才那次跑到哪了」
		var latest UpgradeProgress
		if err := r.get(ctx, "/progress", &latest); err != nil {
			return nil, err
		}
		return &latest, nil
	}
	var p UpgradeProgress
	if err := r.get(ctx, "/progress?id="+id, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *socketRunner) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://updater"+path, nil)
	if err != nil {
		return err
	}
	return r.do(req, out)
}

func (r *socketRunner) post(ctx context.Context, path string, body any, out any) error {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://updater"+path, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return r.do(req, out)
}

// do 发请求并解析响应，把更新器给出的错误信息原样带出来。
func (r *socketRunner) do(req *http.Request, out any) error {
	resp, err := r.client.Do(req)
	if err != nil {
		// 连不上就是「不在场」，用那个专门的错误 ——
		// 它对应的用户动作是「去服务器上装更新器」，
		// 而其它错误对应的动作是「看日志」
		if isConnRefused(err) {
			return ErrUpdaterUnavailable
		}
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		// 更新器把失败原因放在 error 字段里（例如「健康检查未通过，已回滚」），
		// 这句话是最有用的排查线索，必须原样透出而不是替换成「请求失败 500」
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return fmt.Errorf("更新器返回 %d", resp.StatusCode)
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("解析更新器响应失败: %w", err)
	}
	return nil
}

// isConnRefused 判断错误是否是「socket 不存在或没人监听」。
//
// 这两种情况的区别对用户没有意义（都是「更新器不在场」），
// 但要把它们与「更新器在场但请求超时」分开 —— 后者要去看日志。
func isConnRefused(err error) bool {
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT) {
		return true
	}
	// unix socket 的连接失败会被包在 *net.OpError 里，
	// 上面两条 errors.Is 已经能穿透 Unwrap 链匹配到；
	// 这里再兜一层，覆盖平台相关的包装（例如 "connect: connection refused"
	// 在某些版本里只以字符串形式出现）
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		msg := strings.ToLower(opErr.Err.Error())
		return strings.Contains(msg, "no such file") || strings.Contains(msg, "connection refused")
	}
	return false
}
