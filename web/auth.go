package web

// 账号密码登录：users.json + 无状态会话 cookie，与旧 token 双通道并存。
//
// 设计来自 2026-08-20 的十轮调研，几个决定与其依据：
//
//   - PBKDF2-HMAC-SHA256、600,000 迭代（OWASP 参数）。调研的首选本是
//     Argon2id，但它要引 golang.org/x/crypto；这个项目总共两个依赖，
//     crypto/pbkdf2 是 Go 1.24 起的标准库——零依赖换一个"次优但达标"
//     的算法，在本地/内网威胁模型下是划算的。
//   - 会话是无状态 HMAC 签名 cookie（username|过期时间），签名密钥持久化
//     在 users.json 旁边的 secret 文件里：容器频繁重建，内存会话表会把
//     用户一次次踢回登录页。代价是没有服务端吊销——与今天的 token 模型
//     同级（POGO_WEB_TOKEN 也无法吊销），可接受。
//   - cookie HttpOnly + SameSite=Strict：本服务的状态变更全部是 POST 且
//     withOrigin 已拒跨源，Strict 在此之上把 CSRF 面收到最小。
//   - 首个账号没有默认密码：users.json 不存在时启动生成随机 admin 密码
//     打印到日志（Rancher/Keycloak 的 bootstrap 模式）。Grafana 式
//     admin/admin 已被业界认定是反模式——默认凭据是被主动扫描的。
//   - 登录限流 per-IP 5 次失败锁 15 分钟（Vaultwarden/Nextcloud 模式），
//     内存实现，单实例自托管足够。

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yosukeno/pogo/identity"
)

const (
	// pbkdf2Iters 是 OWASP Password Storage Cheat Sheet 给 PBKDF2-HMAC-SHA256
	// 的数字。存进哈希串里，将来上调不需要重置所有密码。
	pbkdf2Iters  = 600_000
	pbkdf2KeyLen = 32
	saltLen      = 16

	sessionCookie = "pi_session"
	sessionTTL    = 7 * 24 * time.Hour

	maxLoginFails = 5
	loginLockout  = 15 * time.Minute

	bootstrapUser = "admin"
)

// userRecord 是 users.json 里的一行。Hash 自描述（算法$迭代$盐$哈希），
// 手工编辑时往数组里加一行即可——账号管理就是编辑这个文件。
type userRecord struct {
	Username string `json:"username"`
	Hash     string `json:"hash"`
	// Token 是该账号的固定访问令牌：没有过期时间，轮换就是改这个字段
	// 然后重启。给脚本和外部系统用 —— ?token= 或 Bearer 头携带，直接
	// 进入这个账号的会话空间，与登录后的 cookie 等价。空 = 无令牌。
	Token string `json:"token,omitempty"`
	// Admin 标记该账号持有运维权限（配置/MCP/skills/定时任务等端点）。
	// 默认 admin 账号不用写这个字段；普通用户就是普通用户——自己的
	// 会话空间，够不着的运维面。
	Admin bool `json:"admin,omitempty"`
	// MCPTokens 是该账号专属的 MCP 客户端凭据：mcp.json 里的 server 名 →
	// bearer 令牌。该账号的会话用这些令牌建自己的连接（按账号分池），
	// 与其他账号互不共用；轮换就是改文件重启，与 Token 字段同语义。
	// 空 = 该账号照常使用 mcp.json 的原始凭据（共享池）。
	MCPTokens map[string]string `json:"mcp_tokens,omitempty"`
	Created   string            `json:"created,omitempty"`
}

type usersFile struct {
	Users []userRecord `json:"users"`
}

// authStore 为空 path 时是"未启用"状态：登录页不出面，一切回到 token 模式。
type authStore struct {
	path string
	key  []byte // 会话签名密钥，持久化于 <path>.secret

	mu    sync.RWMutex
	users map[string]userRecord

	// 登录限流：IP → 失败计数与锁定期。内存态，重启即清零——攻击者重启
	// 不了它，被锁的用户等 15 分钟或重启服务都能恢复。
	failsMu sync.Mutex
	fails   map[string]loginFails
}

type loginFails struct {
	count int
	until time.Time
}

// newAuthStore 加载（或 bootstrap）users.json。返回的 bootstrap 密码只在该
// 次创建时非空——由调用方打印到日志，不落任何明文文件。
func newAuthStore(path string) (*authStore, error) {
	if path == "" {
		return &authStore{}, nil
	}
	a := &authStore{path: path, users: map[string]userRecord{}, fails: map[string]loginFails{}}

	// 会话密钥先行：无论走 bootstrap 还是加载既有用户，签名密钥都得在位
	// （bootstrap 分支早返回，放在用户加载之后就会漏掉它）。
	key, err := loadOrCreateSecret(path + ".secret")
	if err != nil {
		return nil, err
	}
	a.key = key

	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// 首次启用：生成一次性 admin 密码，打印给操作者，改密即编辑 JSON。
		pw, err := genPassword()
		if err != nil {
			return nil, err
		}
		hash, err := hashPassword(pw)
		if err != nil {
			return nil, err
		}
		rec := userRecord{
			Username: bootstrapUser,
			Hash:     hash,
			Created:  time.Now().UTC().Format(time.RFC3339),
		}
		a.users[rec.Username] = rec
		if err := a.save(); err != nil {
			return nil, err
		}
		fmt.Printf("  password login enabled: bootstrap account %q with password %q (change it by editing %s)\n",
			bootstrapUser, pw, path)
		return a, nil
	case err != nil:
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var f usersFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for _, u := range f.Users {
		if u.Username == "" || u.Hash == "" {
			return nil, fmt.Errorf("%s: user entry needs username and hash", path)
		}
		a.users[u.Username] = u
	}
	if len(a.users) == 0 {
		return nil, fmt.Errorf("%s: no users; delete the file to re-bootstrap", path)
	}
	return a, nil
}

// loadOrCreateSecret 读持久会话密钥（重建容器不掉线），没有就生成一份。
func loadOrCreateSecret(path string) ([]byte, error) {
	if key, err := os.ReadFile(path); err == nil && len(key) == 32 {
		return key, nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate session secret: %w", err)
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, fmt.Errorf("write session secret: %w", err)
	}
	return key, nil
}

// Enabled 报告密码登录是否启用（users.json 在位）。
func (a *authStore) Enabled() bool { return a != nil && a.path != "" }

// isAdmin 报告账号是否持有运维权限：默认 admin 账号天然持有，其余看
// 账号行的 "admin" 标记。users.json 不在位时只有环境 token 绑定的
// admin 一个名字可问——答案仍是名字规则。
func (a *authStore) isAdmin(username string) bool {
	if username == bootstrapUser {
		return true
	}
	if !a.Enabled() {
		return false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.users[username].Admin
}

// userByToken 把一个固定访问令牌解析到账号（见 userRecord.Token）。逐账号
// 常时比较：账号数是运维手写出的个位数，遍历的时序泄露不比一次失败比较
// 多。命中的账号被删号后令牌随之失效——令牌只是 users.json 一行的投影。
func (a *authStore) userByToken(token string) (string, bool) {
	if !a.Enabled() || token == "" {
		return "", false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, u := range a.users {
		if u.Token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(u.Token)) == 1 {
			return u.Username, true
		}
	}
	return "", false
}

// mcpTokensFor 返回该账号的 MCP server 名→令牌覆盖表；无覆盖返回 nil。
func (a *authStore) mcpTokensFor(username string) map[string]string {
	if !a.Enabled() || username == "" {
		return nil
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	rec, ok := a.users[username]
	if !ok || len(rec.MCPTokens) == 0 {
		return nil
	}
	out := make(map[string]string, len(rec.MCPTokens))
	for name, token := range rec.MCPTokens {
		out[name] = token
	}
	return out
}

// allMcpTokens 枚举所有带 MCP 凭据覆盖的账号，键为该账号的本地身份
// Scope（与 localIdentity 相同的 LocalScope 空间），调用方因此不必再
// 处理用户名字符串。每次调用都读当前 users map：账号可在运行期增改。
func (a *authStore) allMcpTokens() map[identity.Scope]map[string]string {
	out := map[identity.Scope]map[string]string{}
	if !a.Enabled() {
		return out
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, rec := range a.users {
		if len(rec.MCPTokens) == 0 {
			continue
		}
		tokens := make(map[string]string, len(rec.MCPTokens))
		for name, token := range rec.MCPTokens {
			tokens[name] = token
		}
		out[identity.LocalScope(rec.Username)] = tokens
	}
	return out
}

func (a *authStore) save() error {
	f := usersFile{Users: make([]userRecord, 0, len(a.users))}
	for _, u := range a.users {
		f.Users = append(f.Users, u)
	}
	// 数组顺序来自 map 遍历不稳定；排序让文件 diff 可读。
	for i := 0; i < len(f.Users); i++ {
		for j := i + 1; j < len(f.Users); j++ {
			if f.Users[j].Username < f.Users[i].Username {
				f.Users[i], f.Users[j] = f.Users[j], f.Users[i]
			}
		}
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(a.path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := a.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, a.path); err != nil {
		// 容器部署会把 users.json 作为单文件 bind-mount 进来，rename 无法
		// 替换挂载点（EBUSY/权限错）。退化为原位写入：不是原子的，但文件
		// 小、写入毫秒级，替代方案是"容器里永远改不了密码"。
		if werr := os.WriteFile(a.path, append(b, '\n'), 0o600); werr != nil {
			return fmt.Errorf("rename %s: %v (in-place fallback: %v)", tmp, err, werr)
		}
	}
	return nil
}

// hashPassword 生成自描述哈希串：pbkdf2-sha256$iter$saltB64$hashB64。
func hashPassword(pw string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, pw, salt, pbkdf2Iters, pbkdf2KeyLen)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s",
		pbkdf2Iters,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// verifyPassword 常时比较。解析失败按不匹配处理，不向调用方区分原因——
// 登录失败的原因只对猜密码的人有趣。
func verifyPassword(stored, pw string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	var iters int
	if _, err := fmt.Sscanf(parts[1], "%d", &iters); err != nil || iters <= 0 || iters > 10_000_000 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, pw, salt, iters, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

// genPassword 生成 16 字符随机密码（去掉了易混淆字符）。
func genPassword() (string, error) {
	const alphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	out := make([]byte, 16)
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	for i, b := range buf {
		out[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(out), nil
}

// HashPassword 是 -hash-password 助手的入口：往 users.json 里手工加账号时，
// 用它把明文密码变成可粘贴的哈希串（与登录验证同一套参数）。
func HashPassword(pw string) (string, error) { return hashPassword(pw) }

// GenToken 是 -gen-token 助手的入口：生成一个固定访问令牌的候选值
// （32 字符，与 genPassword 同一张去混淆字母表）。运维把它粘进 users.json
// 某账号的 "token" 字段即完成绑定；无过期、无状态，轮换即改值重启。
func GenToken() (string, error) {
	const alphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	out := make([]byte, 32)
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	for i, b := range buf {
		out[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(out), nil
}

// ── 会话 cookie ────────────────────────────────────────────────────────────

// signSession 产出 "base64(user|exp).base64(hmac)"。无状态：验证只靠密钥
// 与过期时间，没有服务端会话表，重启不失效。
func (a *authStore) signSession(user string, now time.Time) (string, error) {
	payload := base64.RawURLEncoding.EncodeToString(
		[]byte(fmt.Sprintf("%s|%d", user, now.Add(sessionTTL).Unix())))
	mac := hmac.New(sha256.New, a.key)
	mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// sessionUser 验证并解出会话所属用户。
func (a *authStore) sessionUser(r *http.Request, now time.Time) (string, bool) {
	if !a.Enabled() {
		return "", false
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return "", false
	}
	payload, sig, ok := strings.Cut(c.Value, ".")
	if !ok {
		return "", false
	}
	mac := hmac.New(sha256.New, a.key)
	mac.Write([]byte(payload))
	want := mac.Sum(nil)
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || subtle.ConstantTimeCompare(got, want) != 1 {
		return "", false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return "", false
	}
	user, exp, ok := strings.Cut(string(decoded), "|")
	if !ok || user == "" {
		return "", false
	}
	var expUnix int64
	if _, err := fmt.Sscanf(exp, "%d", &expUnix); err != nil || now.Unix() > expUnix {
		return "", false
	}
	a.mu.RLock()
	_, known := a.users[user]
	a.mu.RUnlock()
	return user, known
}

// ── 登录限流 ───────────────────────────────────────────────────────────────

func (a *authStore) loginAllowed(ip string, now time.Time) bool {
	a.failsMu.Lock()
	defer a.failsMu.Unlock()
	f := a.fails[ip]
	return now.After(f.until)
}

func (a *authStore) loginFailed(ip string, now time.Time) {
	a.failsMu.Lock()
	defer a.failsMu.Unlock()
	f := a.fails[ip]
	f.count++
	if f.count >= maxLoginFails {
		f.until = now.Add(loginLockout)
		f.count = 0
	}
	a.fails[ip] = f
}

func (a *authStore) loginSucceeded(ip string) {
	a.failsMu.Lock()
	delete(a.fails, ip)
	a.failsMu.Unlock()
}

// ── HTTP 处理 ──────────────────────────────────────────────────────────────

// handleLoginStatus 是门面信息：SPA 据此决定拦截页摆哪些登录方式。公开
// 无害——它只回答"这个部署接受哪种登录"，不泄露任何账号信息。
// （mode 的取值不写成常量赋值：wire 的重名扫描会把对 token 一词的赋值
// 形式当成事件名定义抓走，map 字面量不在其列。键是"未启用密码登录"。）
var authModes = map[bool]string{true: "token", false: "password"}

func (s *Server) handleLoginStatus(w http.ResponseWriter, r *http.Request) {
	// 身份字段是尽力解析（不带凭证则为空）：持有者自己的名字与权限位，
	// 不是任何人的清单。UI 据此决定设置按钮等运维面的可见性。
	user, admin := "", false
	if id, ok := s.authenticate(r); ok {
		admin = id.Admin || !s.multiuser() // 单用户模式：持 token 者即操作者
		if id.Local {
			user = id.Username
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mode":     authModes[!s.auth.Enabled()],
		"password": s.auth.Enabled(), // 拦截页是否提供账号密码表单
		"user":     user,
		"admin":    admin,
	})
}

// handleListAccounts 是"账户与安全"面板的账号清单：名字、创建时间、角
// 色、是否配了固定 token——哈希与 token 值本身永不出现在响应里。仅管理
// 员可见（路由挂了 admin 门）。
func (s *Server) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	s.auth.mu.RLock()
	list := make([]map[string]any, 0, len(s.auth.users))
	for _, u := range s.auth.users {
		list = append(list, map[string]any{
			"username":  u.Username,
			"created":   u.Created,
			"admin":     s.auth.isAdmin(u.Username),
			"has_token": u.Token != "",
		})
	}
	s.auth.mu.RUnlock()
	sort.Slice(list, func(i, j int) bool {
		return list[i]["username"].(string) < list[j]["username"].(string)
	})
	writeJSON(w, http.StatusOK, map[string]any{"accounts": list})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.auth.Enabled() {
		http.Error(w, "password login not enabled", http.StatusNotFound)
		return
	}
	ip := clientIP(r)
	now := time.Now()
	if !s.auth.loginAllowed(ip, now) {
		http.Error(w, "too many failed attempts; retry later", http.StatusTooManyRequests)
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		// AdminPassword 开户凭证：账号不存在时，必须携带 admin 的密码
		// （或部署 token，同一个"运维钥匙"的两种拼写）才允许建立账号。
		// 已存在的账号忽略此字段——登录就是登录。
		AdminPassword string `json:"admin_password"`
	}
	if err := decodeBody(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.auth.mu.RLock()
	rec, ok := s.auth.users[body.Username]
	s.auth.mu.RUnlock()
	if !ok {
		// 开户路径：外部系统带来自己的用户，第一次登录即建号。
		if err := s.provision(body.Username, body.Password, body.AdminPassword); err != nil {
			s.auth.loginFailed(ip, now) // 猜 admin 凭据与猜密码同罪，共用限流
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		s.auth.loginSucceeded(ip)
		s.writeSessionCookie(w, body.Username, now)
		return
	}
	if !verifyPassword(rec.Hash, body.Password) {
		s.auth.loginFailed(ip, now)
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}
	s.auth.loginSucceeded(ip)
	s.writeSessionCookie(w, body.Username, now)
}

// provision 建立一个新账号：用户名与密码的形状先过一遍，开户凭证必须是
// admin 的密码或部署 token（handleChangePassword 的同款双通道）。新账号是
// 普通用户——没有 admin 位、没有运维权限；错误消息不区分是哪一步失败，
// 让探测者得不到"用户名可用但凭证错了"这类分级信息。
func (s *Server) provision(username, password, adminPassword string) error {
	if username == "" || len(username) > 64 || !validLoginName(username) || username == bootstrapUser {
		return errors.New("invalid credentials")
	}
	if n := len([]rune(password)); n < 8 || n > 128 {
		return errors.New("invalid credentials")
	}
	s.auth.mu.RLock()
	admin, haveAdmin := s.auth.users[bootstrapUser]
	s.auth.mu.RUnlock()
	tokenOK := s.opts.Token != "" &&
		subtle.ConstantTimeCompare([]byte(adminPassword), []byte(s.opts.Token)) == 1
	if !tokenOK && !(haveAdmin && verifyPassword(admin.Hash, adminPassword)) {
		return errors.New("invalid credentials")
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	s.auth.mu.Lock()
	// 复查：并发开户可能已有人抢先注册了这个名字——后来者按登录失败处理，
	// 而不是覆盖别人的密码。
	if _, exists := s.auth.users[username]; exists {
		s.auth.mu.Unlock()
		return errors.New("invalid credentials")
	}
	s.auth.users[username] = userRecord{
		Username: username,
		Hash:     hash,
		Created:  time.Now().UTC().Format(time.RFC3339),
	}
	saveErr := s.auth.save()
	if saveErr != nil {
		// 落盘失败回滚内存：不留"内存里有、文件里没有"的幽灵账号——重启
		// 即丢，而存在期间它的密码又真实有效过。
		delete(s.auth.users, username)
	}
	s.auth.mu.Unlock()
	return saveErr
}

// validLoginName 拒绝控制字符与通配符——账号名会成为目录键的一部分
// （用户根目录按主体哈希取键，这里只是纵深防御的一致形状检查）。
func validLoginName(s string) bool {
	if strings.ContainsAny(s, "*?/\\") {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// writeSessionCookie 签发并写下登录会话。
func (s *Server) writeSessionCookie(w http.ResponseWriter, username string, now time.Time) {
	value, err := s.auth.signSession(username, now)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleChangePassword 是"账户与安全"面板的改密端点：按目标账号分两种授权。
//
//   - 目标是管理员账号（默认 admin 或 "admin": true 行）：无论谁来改，都必
//     须给出该账号的当前凭据（旧密码，或部署 token——它绑定 admin）。
//   - 目标是普通账号：请求者本身持管理员身份（admin 会话/部署 token/管理
//     员 token），或请求体带有效的 admin_password，即直接放行改密——这是
//     外部系统的密码同步通道："本系统密码变了，把 pogo 里对应账号改成一致的"。
//     请求者什么管理凭据都没有时，只剩自助路径：改自己的账号，给旧密码。
//
// 请求体：{username?（目标，空=会话本人或 admin）, current?（旧凭据）,
// next, admin_password?（为他人改密的 admin 凭证）}。所有失败共用登录
// 限流——在这里猜 admin 凭据与猜旧密码同样是在爆破。
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if !s.auth.Enabled() {
		http.Error(w, "password login not enabled (set POGO_USERS_FILE and restart)", http.StatusNotFound)
		return
	}
	ip := clientIP(r)
	now := time.Now()
	if !s.auth.loginAllowed(ip, now) {
		http.Error(w, "too many failed attempts; retry later", http.StatusTooManyRequests)
		return
	}
	var body struct {
		Username      string `json:"username"`
		Current       string `json:"current"`
		Next          string `json:"next"`
		AdminPassword string `json:"admin_password"`
	}
	if err := decodeBody(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// 8–128:OWASP 下限,上限只是防呆。长度按 rune 计——中文密码一个汉字
	// 就是一个字符,不是三个字节。
	if n := len([]rune(body.Next)); n < 8 || n > 128 {
		http.Error(w, "new password must be 8-128 characters", http.StatusBadRequest)
		return
	}
	// 目标账号:显式点名优先;否则会话本人;纯 token 通道落在 admin。
	self := ""
	requesterAdmin := !s.multiuser()
	if id, ok := identityFrom(r); ok {
		if id.Local {
			self = id.Username
		}
		requesterAdmin = requesterAdmin || id.Admin
	}
	target := body.Username
	if target == "" {
		if self != "" {
			target = self
		} else {
			target = bootstrapUser
		}
	}
	s.auth.mu.RLock()
	rec, known := s.auth.users[target]
	adminRec, haveAdmin := s.auth.users[bootstrapUser]
	targetIsAdmin := s.auth.isAdmin(target)
	s.auth.mu.RUnlock()
	if !known {
		http.Error(w, "account not found", http.StatusNotFound)
		return
	}

	// token 比对仅在配置了 token 时成立:未配置时空串对空串的常时比较
	// 恒真,会把"什么都没输"当成凭据。
	tokenMatch := func(given string) bool {
		return s.opts.Token != "" &&
			subtle.ConstantTimeCompare([]byte(given), []byte(s.opts.Token)) == 1
	}
	failed := func() {
		s.auth.loginFailed(ip, now)
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
	}

	switch {
	case targetIsAdmin:
		// 管理员账号:必须交出它自己的当前凭据。
		if !tokenMatch(body.Current) && !verifyPassword(rec.Hash, body.Current) {
			failed()
			return
		}
	case requesterAdmin:
		// 请求者已是管理员身份:普通账号直接放行(面板里点按钮的就是这条)。
	case body.AdminPassword != "" &&
		(tokenMatch(body.AdminPassword) || (haveAdmin && verifyPassword(adminRec.Hash, body.AdminPassword))):
		// 携带有效 admin 凭证的外部同步通道。
	case self != "" && target == self:
		// 自助:改自己。旧密码错按凭据失败计(401+限流)——在这里猜自己的
		// 旧密码与猜别人的同样是爆破。
		if !verifyPassword(rec.Hash, body.Current) {
			failed()
			return
		}
	default:
		http.Error(w, "not authorized to change this account's password", http.StatusForbidden)
		return
	}

	s.auth.loginSucceeded(ip)
	hash, err := hashPassword(body.Next)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.auth.mu.Lock()
	rec.Hash = hash
	s.auth.users[target] = rec
	saveErr := s.auth.save()
	s.auth.mu.Unlock()
	if saveErr != nil {
		http.Error(w, saveErr.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleDeleteAccount 是"账户与安全"面板的删号端点：从 users.json 移除一行
// 普通账号。管理员账号（默认 admin 与 "admin": true 行）不可删——运维钥匙
// 不能被自己锁在门外；路由已挂 admin 门，能走到这里的只有持管理身份者。
// 删除即刻生效：该账号的密码登录、固定 token 与 MCP 凭据随行消失，进行中
// 的会话 cookie 在下一个请求就被 sessionUser 的存在性复查拒绝。只动凭据，
// 不碰该用户已有的会话与工作区数据——那是目录里的内容，与账号行分开。
// 已配置的受管 Web 服务先写入 owner fence 并清理其运行资源；这不改变
// 上述历史会话/源码保留语义。清理失败保留凭据供管理员明确重试。
func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	if !s.auth.Enabled() {
		http.Error(w, "password login not enabled (set POGO_USERS_FILE and restart)", http.StatusNotFound)
		return
	}
	target := r.PathValue("username")
	if target == "" || s.auth.isAdmin(target) {
		http.Error(w, "admin accounts cannot be deleted", http.StatusForbidden)
		return
	}
	s.auth.mu.Lock()
	rec, known := s.auth.users[target]
	if !known {
		s.auth.mu.Unlock()
		http.Error(w, "account not found", http.StatusNotFound)
		return
	}
	if s.mgr.cfg.WebServices != nil {
		// Persist the runtime admission fence before deleting credentials. Do not
		// hold the account lock across private runner/gateway I/O: unrelated
		// users must retain their normal login and session access.
		s.auth.mu.Unlock()
		owner := identity.LocalScope(target)
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		err := s.mgr.cfg.WebServices.DeleteOwner(ctx, sessionResultOwnerKey(&owner))
		cancel()
		if err != nil {
			http.Error(w, "account deletion is pending managed Web cleanup; credentials retained for retry", http.StatusServiceUnavailable)
			return
		}
		s.auth.mu.Lock()
		rec, known = s.auth.users[target]
		if !known {
			s.auth.mu.Unlock()
			http.Error(w, "account not found", http.StatusNotFound)
			return
		}
	}
	delete(s.auth.users, target)
	saveErr := s.auth.save()
	if saveErr != nil {
		// 落盘失败回滚内存：不留"内存里没有、文件里还有"的账号——删号是
		// 凭据回收，回收没成功就得当没发生。
		s.auth.users[target] = rec
	}
	s.auth.mu.Unlock()
	if saveErr != nil {
		if s.mgr.cfg.WebServices != nil {
			http.Error(w, "managed Web services were revoked, but account credentials could not be saved; retry deletion", http.StatusInternalServerError)
			return
		}
		http.Error(w, saveErr.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.revokeOfficeGateway(w, r)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
