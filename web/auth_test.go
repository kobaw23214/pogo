package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/catalog"
	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
)

// The auth tests build the store directly (hash/session/limiter units) and the
// login flow through a real server (httptest), because the middleware chain is
// where the two channels — cookie and legacy token — actually meet.

func TestHashPasswordRoundTrip(t *testing.T) {
	h, err := hashPassword("s3cret!")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "pbkdf2-sha256$") {
		t.Fatalf("hash not self-describing: %q", h)
	}
	if !verifyPassword(h, "s3cret!") {
		t.Fatal("correct password rejected")
	}
	if verifyPassword(h, "s3cret?") || verifyPassword(h, "") {
		t.Fatal("wrong password accepted")
	}
	// 相同密码两次哈希结果不同（盐在起作用）
	h2, _ := hashPassword("s3cret!")
	if h == h2 {
		t.Fatal("salt reuse: identical hashes for identical passwords")
	}
	for _, bad := range []string{"", "plain", "pbkdf2-sha256$abc", "md5$1$xx$yy"} {
		if verifyPassword(bad, "s3cret!") {
			t.Fatalf("malformed hash accepted: %q", bad)
		}
	}
}

func TestSessionSignVerify(t *testing.T) {
	a := &authStore{path: "x", key: []byte("0123456789abcdef0123456789abcdef"), users: map[string]userRecord{"u": {}}}
	now := time.Now()
	v, err := a.signSession("u", now)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: v})
	if user, ok := a.sessionUser(r, now); !ok || user != "u" {
		t.Fatalf("sessionUser = %q,%v want u,true", user, ok)
	}
	if _, ok := a.sessionUser(r, now.Add(sessionTTL+time.Minute)); ok {
		t.Fatal("expired session accepted")
	}
	for _, tampered := range []string{v + "x", "YQ.YQ", strings.SplitN(v, ".", 2)[1] + "." + strings.SplitN(v, ".", 2)[0]} {
		r2 := httptest.NewRequest("GET", "/", nil)
		r2.AddCookie(&http.Cookie{Name: sessionCookie, Value: tampered})
		if _, ok := a.sessionUser(r2, now); ok {
			t.Fatalf("tampered session accepted: %q", tampered)
		}
	}
	// 用户已被删（users.json 编辑后）：会话随之作废
	delete(a.users, "u")
	if _, ok := a.sessionUser(r, now); ok {
		t.Fatal("session of removed user accepted")
	}
}

func TestLoginRateLimit(t *testing.T) {
	a := &authStore{fails: map[string]loginFails{}}
	now := time.Now()
	for i := 0; i < maxLoginFails; i++ {
		if !a.loginAllowed("1.2.3.4", now) {
			t.Fatalf("locked after %d fails, want %d", i, maxLoginFails)
		}
		a.loginFailed("1.2.3.4", now)
	}
	if a.loginAllowed("1.2.3.4", now) {
		t.Fatal("no lockout after max fails")
	}
	if !a.loginAllowed("1.2.3.4", now.Add(loginLockout+time.Second)) {
		t.Fatal("lockout did not expire")
	}
	if !a.loginAllowed("5.6.7.8", now) {
		t.Fatal("one IP's lockout hit another IP")
	}
}

// authHarness 建一个启用密码登录的真实服务器：users.json 放两个已知账号
// （alice 已知密码；bootstrap admin 的随机密码不出场）。
func authHarness(t *testing.T) (*httptest.Server, string) {
	ts, _, tok := authHarnessWithServer(t)
	return ts, tok
}

// authHarnessWithServer 额外交出 *Server：需要直接布景 users.json 行的测试
// （删号、令牌回收）从这里走，其余调用方仍用 authHarness 的两返回值形态。
func authHarnessWithServer(t *testing.T) (*httptest.Server, *Server, string) {
	t.Helper()
	t.Setenv("KIMI_API_KEY", "test-key")
	installTestCatalog(t)

	dir := t.TempDir()
	usersPath := filepath.Join(dir, "users.json")
	// 密码登录现在隐含按用户隔离：Manager 必须带上归属目录，NewServer 才接受。
	cat, err := catalog.Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cat.Close() })
	mgr, err := NewManager(Config{
		Cwd:         t.TempDir(),
		SessionDir:  t.TempDir(),
		Catalog:     cat,
		Model:       "k3",
		MaxTurns:    5,
		GateTimeout: 5 * time.Second,
		RunTimeout:  time.Minute,
		IdleTimeout: time.Hour,
		NewClient: func(c config.Resolved, _ func(llm.RetryInfo)) llm.Client {
			return &scriptedClient{model: c.Model}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)

	srv, err := NewServer(mgr, ServerOptions{Token: "test-token", UsersFile: usersPath})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := hashPassword("pw-test-1")
	if err != nil {
		t.Fatal(err)
	}
	srv.auth.mu.Lock()
	srv.auth.users["alice"] = userRecord{Username: "alice", Hash: hash}
	srv.auth.save()
	srv.auth.mu.Unlock()

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, srv, "test-token"
}

func TestLoginFlowEndToEnd(t *testing.T) {
	srv, tok := authHarness(t)
	client := srv.Client()
	client.Jar, _ = cookiejar.New(nil)

	// 状态端点公开且报告 password 模式
	res, err := client.Get(srv.URL + "/api/auth/status")
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("status: %v %v", err, res.StatusCode)
	}
	var st struct {
		Mode string `json:"mode"`
	}
	json.NewDecoder(res.Body).Decode(&st)
	res.Body.Close()
	if st.Mode != "password" {
		t.Fatalf("mode = %q, want password", st.Mode)
	}

	// 错密码 401；不带任何凭证的 API 401
	res, _ = client.Post(srv.URL+"/api/login", "application/json",
		strings.NewReader(`{"username":"alice","password":"wrong"}`))
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("wrong password = %d, want 401", res.StatusCode)
	}
	res, _ = client.Get(srv.URL + "/api/models")
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("no credentials = %d, want 401", res.StatusCode)
	}

	// 对密码 200 + 会话 cookie，之后 API 经 cookie 通
	res, _ = client.Post(srv.URL+"/api/login", "application/json",
		strings.NewReader(`{"username":"alice","password":"pw-test-1"}`))
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("login = %d, want 200", res.StatusCode)
	}
	res, _ = client.Get(srv.URL + "/api/models")
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("cookie auth = %d, want 200", res.StatusCode)
	}

	// 共享 token 现在绑定默认 admin：裸 client（不挂 jar）只带 token 也通，
	// 且拿到的是 admin 的身份与权限（越权面见 TestMultiuserTokenChannels）。
	req, _ := http.NewRequest("GET", srv.URL+"/api/models", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	res2, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res2.Body.Close()
	if res2.StatusCode != 200 {
		t.Fatalf("admin-bound shared token = %d, want 200", res2.StatusCode)
	}
}

// 限流端到端：maxLoginFails 次失败后，即使密码正确也 429。
func TestLoginLockoutEndToEnd(t *testing.T) {
	srv, _ := authHarness(t)
	client := srv.Client()
	client.Jar, _ = cookiejar.New(nil)
	for i := 0; i < maxLoginFails; i++ {
		res, _ := client.Post(srv.URL+"/api/login", "application/json",
			strings.NewReader(`{"username":"alice","password":"wrong"}`))
		res.Body.Close()
		if res.StatusCode != 401 {
			t.Fatalf("fail %d = %d, want 401", i, res.StatusCode)
		}
	}
	res, _ := client.Post(srv.URL+"/api/login", "application/json",
		strings.NewReader(`{"username":"alice","password":"pw-test-1"}`))
	res.Body.Close()
	if res.StatusCode != 429 {
		t.Fatalf("locked login with correct password = %d, want 429", res.StatusCode)
	}
}

// 改密端到端：原密码或 token 二选一作为当前凭据，新哈希即刻生效。
func TestChangePasswordEndToEnd(t *testing.T) {
	srv, tok := authHarness(t)
	client := srv.Client()
	client.Jar, _ = cookiejar.New(nil)

	post := func(body string) int {
		res, err := client.Post(srv.URL+"/api/auth/password", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	loginCode := func(user, pw string) int {
		res, err := client.Post(srv.URL+"/api/login", "application/json",
			strings.NewReader(`{"username":"`+user+`","password":"`+pw+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}

	// 会话在身：alice 登录后改自己的密码。错的当前凭据 401、过短的新
	// 密码 400、原密码改密 200，旧密码即刻失效。
	if code := loginCode("alice", "pw-test-1"); code != 200 {
		t.Fatalf("precondition login = %d", code)
	}
	if code := post(`{"current":"nope-nope","next":"new-pw-42!"}`); code != 401 {
		t.Fatalf("wrong current = %d, want 401", code)
	}
	if code := post(`{"current":"pw-test-1","next":"short"}`); code != 400 {
		t.Fatalf("too-short new = %d, want 400", code)
	}
	if code := post(`{"current":"pw-test-1","next":"new-pw-42!"}`); code != 200 {
		t.Fatalf("change with old password = %d, want 200", code)
	}
	if code := loginCode("alice", "pw-test-1"); code != 401 {
		t.Fatalf("old password still valid = %d, want 401", code)
	}
	if code := loginCode("alice", "new-pw-42!"); code != 200 {
		t.Fatalf("new password rejected = %d, want 200", code)
	}

	// token 兼作当前凭据的恢复路径回来了：token 即 admin，target 也落在
	// admin——bootstrap 随机密码丢了就靠它重设。
	req, _ := http.NewRequest("POST", srv.URL+"/api/auth/password",
		strings.NewReader(`{"current":"`+tok+`","next":"admin-pw-7!"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	res, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("token-borne change = %d, want 200", res.StatusCode)
	}
	if code := loginCode("admin", "admin-pw-7!"); code != 200 {
		t.Fatalf("admin's token-set password rejected = %d", code)
	}
}

// 未启用密码登录（无 UsersFile）时改密 404：带着合法 token 来也没有可改
// 的密码——门在 handler 之前，404 是 handler 自己的答复，不是中间件的。
func TestChangePasswordOffByDefault(t *testing.T) {
	front, _ := panelHarness(t, nil)
	req, _ := http.NewRequest("POST", front.URL+"/api/auth/password",
		strings.NewReader(`{"current":"whatever","next":"whatever-99!"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	res, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 404 {
		t.Fatalf("change without users file = %d, want 404", res.StatusCode)
	}
}

// 未启用（无 UsersFile）时一切回到 token 模式：状态端点说 token，登录 404。
func TestPasswordModeOffByDefault(t *testing.T) {
	front, _ := panelHarness(t, nil)
	res, err := http.Get(front.URL + "/api/auth/status")
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		Mode string `json:"mode"`
	}
	json.NewDecoder(res.Body).Decode(&st)
	res.Body.Close()
	if st.Mode != "token" {
		t.Fatalf("mode = %q, want token", st.Mode)
	}
	res, _ = http.Post(front.URL+"/api/login", "application/json",
		strings.NewReader(`{"username":"a","password":"b"}`))
	res.Body.Close()
	if res.StatusCode != 404 {
		t.Fatalf("login without users file = %d, want 404", res.StatusCode)
	}
}

// 开户端到端：外部系统带来自己的用户，第一次登录即建号——但必须附带
// admin 凭证（admin 密码或部署 token）。错误凭证不建号、限流计数；
// 建好的账号是普通用户：能登录、有自己的会话，运维端点 403。
func TestLoginProvisionsUnknownAccountWithAdminCredential(t *testing.T) {
	srv, tok := authHarness(t)
	client := srv.Client()
	client.Jar, _ = cookiejar.New(nil)

	post := func(body string) (*http.Response, error) {
		return client.Post(srv.URL+"/api/login", "application/json", strings.NewReader(body))
	}

	// 错误的 admin 凭证：401，且不建号。
	res, err := post(`{"username":"newuser","password":"new-pw-123!","admin_password":"wrong"}`)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("provision with wrong admin credential = %d, want 401", res.StatusCode)
	}
	// 不带 admin 凭证同样 401（一个体面的默认，而不是“缺字段”的 400——
	// 这里不向探测者区分失败原因）。
	res, err = post(`{"username":"newuser","password":"new-pw-123!"}`)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("provision without admin credential = %d, want 401", res.StatusCode)
	}

	// 部署 token 是同一种 admin 凭证：建号 + 登录 cookie 即刻生效。
	res, err = post(`{"username":"newuser","password":"new-pw-123!","admin_password":"` + tok + `"}`)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("provision with deployment token = %d, want 200", res.StatusCode)
	}
	res, err = client.Get(srv.URL + "/api/models")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("new account's cookie = %d, want 200", res.StatusCode)
	}
	// 普通用户：运维端点 403。
	res, err = client.Get(srv.URL + "/api/config")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatalf("provisioned account on operator endpoint = %d, want 403", res.StatusCode)
	}

	// 账号落盘：下次不带 admin 凭证也能直接登录。
	res, err = post(`{"username":"newuser","password":"new-pw-123!"}`)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("re-login provisioned account = %d, want 200", res.StatusCode)
	}

	// 弱密码不建号：形状在凭证检查之前就该被拒。
	res, err = post(`{"username":"weakling","password":"short","admin_password":"` + tok + `"}`)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("provision with weak password = %d, want 401", res.StatusCode)
	}
	res, err = post(`{"username":"weakling","password":"long-enough-99!","admin_password":"` + tok + `"}`)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("weakling with a proper password = %d, want 200", res.StatusCode)
	}
}

// "账户与安全"面板的后端契约：账号清单仅管理员可见，且哈希与 token 值
// 永不出现在响应里；authStatus 带上持有者自己的身份与权限位。
func TestAccountsPanelListsAccountsForAdminOnly(t *testing.T) {
	srv, tok := authHarness(t)
	alice := &http.Client{Jar: func() http.CookieJar { j, _ := cookiejar.New(nil); return j }()}
	res, err := alice.Post(srv.URL+"/api/login", "application/json",
		strings.NewReader(`{"username":"alice","password":"pw-test-1"}`))
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("alice login: %v %v", err, res)
	}
	res.Body.Close()

	// 普通账号：清单 403，身份字段如实。
	res, err = alice.Get(srv.URL + "/api/accounts")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatalf("alice on /api/accounts = %d, want 403", res.StatusCode)
	}
	res, err = alice.Get(srv.URL + "/api/auth/status")
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		User  string `json:"user"`
		Admin bool   `json:"admin"`
	}
	json.NewDecoder(res.Body).Decode(&st)
	res.Body.Close()
	if st.User != "alice" || st.Admin {
		t.Fatalf("alice's status = %+v, want user=alice admin=false", st)
	}

	// 部署 token（=admin）：清单 200，包含 admin 与 alice，绝无哈希或 token 值。
	req, _ := http.NewRequest("GET", srv.URL+"/api/accounts", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	res, err = (&http.Client{}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("admin on /api/accounts = %d, want 200", res.StatusCode)
	}
	raw, _ := io.ReadAll(res.Body)
	body := string(raw)
	for _, secret := range []string{"pbkdf2", `"token":"`, "pogo-timing-pad"} {
		if strings.Contains(body, secret) {
			t.Fatalf("accounts response leaks %q: %s", secret, body)
		}
	}
	var out struct {
		Accounts []struct {
			Username string `json:"username"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, a := range out.Accounts {
		names[a.Username] = true
	}
	if !names["admin"] || !names["alice"] {
		t.Fatalf("accounts = %v, want admin and alice", names)
	}

	// token 的身份字段：绑定到 admin。
	req, _ = http.NewRequest("GET", srv.URL+"/api/auth/status", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	res, err = (&http.Client{}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	json.NewDecoder(res.Body).Decode(&st)
	res.Body.Close()
	if st.User != "admin" || !st.Admin {
		t.Fatalf("token status = %+v, want user=admin admin=true", st)
	}
}

// 管理员为他人改密的三种形态：admin 会话/token 直接放行普通账号（面板
// 按钮与外部系统密码同步共用）；无管理凭据碰他人账号 403；admin 账号
// 本身永远要交出自己的当前凭据——admin_password 不能替代。
func TestChangePasswordByAdminCredentialForOtherAccounts(t *testing.T) {
	srv, tok := authHarness(t)
	post := func(client *http.Client, body string) int {
		res, err := client.Post(srv.URL+"/api/auth/password", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	loginCode := func(user, pw string) int {
		res, err := (&http.Client{}).Post(srv.URL+"/api/login", "application/json",
			strings.NewReader(`{"username":"`+user+`","password":"`+pw+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}

	// 开一个 bob 作为目标账号（登录即开户端点）。
	res0, err := (&http.Client{}).Post(srv.URL+"/api/login", "application/json",
		strings.NewReader(`{"username":"bob","password":"bob-pass-001!","admin_password":"`+tok+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	res0.Body.Close()
	if res0.StatusCode != 200 {
		t.Fatalf("provision bob = %d, want 200", res0.StatusCode)
	}

	// alice（普通会话）碰 bob：403。
	alice := &http.Client{Jar: func() http.CookieJar { j, _ := cookiejar.New(nil); return j }()}
	if res, err := alice.Post(srv.URL+"/api/login", "application/json",
		strings.NewReader(`{"username":"alice","password":"pw-test-1"}`)); err != nil || res.StatusCode != 200 {
		t.Fatalf("alice login: %v %v", err, res)
	} else {
		res.Body.Close()
	}
	if code := post(alice, `{"username":"bob","next":"bob-pass-002!"}`); code != 403 {
		t.Fatalf("alice changes bob without admin credential = %d, want 403", code)
	}

	// 带 admin 凭证（部署 token 拼写）→ 通过，bob 的新密码即刻生效。
	if code := post(alice, `{"username":"bob","next":"bob-pass-002!","admin_password":"`+tok+`"}`); code != 200 {
		t.Fatalf("alice changes bob with admin credential = %d, want 200", code)
	}
	if code := loginCode("bob", "bob-pass-001!"); code != 401 {
		t.Fatalf("bob's old password still valid = %d", code)
	}
	if code := loginCode("bob", "bob-pass-002!"); code != 200 {
		t.Fatalf("bob's new password rejected = %d", code)
	}

	// 纯 token 通道（无会话）直接改普通账号：外部系统同步形态。
	bearer := &http.Client{}
	req, _ := http.NewRequest("POST", srv.URL+"/api/auth/password",
		strings.NewReader(`{"username":"bob","next":"bob-pass-003!"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	res, err := bearer.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("token-borne sync change = %d, want 200", res.StatusCode)
	}
	if code := loginCode("bob", "bob-pass-003!"); code != 200 {
		t.Fatalf("bob after sync change = %d", code)
	}

	// admin 账号本身：admin_password 不能替代它自己的当前凭据。
	if code := post(alice, `{"username":"admin","next":"admin-pw-88!","admin_password":"`+tok+`"}`); code != 401 {
		t.Fatalf("admin target without its own current = %d, want 401", code)
	}
}

// 删号端到端：管理员删普通账号即刻回收其全部凭据（登录、固定 token），
// 管理员行（默认 admin 与 "admin": true）拒绝删除，普通会话碰不到端点，
// 不存在的账号 404。最后记录一个现状交互：开户通道（登录即建号）仍在，
// 被删账号可凭 admin 凭据重新自建——凭据回收不等于名单封禁。
func TestDeleteAccountEndToEnd(t *testing.T) {
	ts, srv, tok := authHarnessWithServer(t)

	// 布景：carol 带固定 token；root 是 "admin": true 行。
	hash, err := hashPassword("pw-carol-1")
	if err != nil {
		t.Fatal(err)
	}
	srv.auth.mu.Lock()
	srv.auth.users["carol"] = userRecord{Username: "carol", Hash: hash, Token: "carol-fixed-token"}
	srv.auth.users["root"] = userRecord{Username: "root", Hash: hash, Admin: true}
	srv.auth.save()
	srv.auth.mu.Unlock()

	plain := &http.Client{}
	del := func(client *http.Client, name string) int {
		req, _ := http.NewRequest("DELETE", ts.URL+"/api/accounts/"+name, nil)
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	// 部署 token（=admin）形态的删号：面板按钮走的同一条通道。
	delToken := func(name string) int {
		req, _ := http.NewRequest("DELETE", ts.URL+"/api/accounts/"+name, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		res, err := plain.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	code := func(method, path, token string) int {
		req, _ := http.NewRequest(method, ts.URL+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := plain.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}

	// 普通会话：删号端点是运维面，403。
	alice := ts.Client()
	alice.Jar, _ = cookiejar.New(nil)
	res, _ := alice.Post(ts.URL+"/api/login", "application/json",
		strings.NewReader(`{"username":"alice","password":"pw-test-1"}`))
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("alice login = %d, want 200", res.StatusCode)
	}
	if c := del(alice, "carol"); c != 403 {
		t.Fatalf("alice on delete = %d, want 403", c)
	}

	// 管理员行拒删：默认 admin 与 "admin": true 的 root。
	if c := delToken("admin"); c != 403 {
		t.Fatalf("delete bootstrap admin = %d, want 403", c)
	}
	if c := delToken("root"); c != 403 {
		t.Fatalf("delete admin-flagged row = %d, want 403", c)
	}
	// 不存在的账号 404。
	if c := delToken("ghost"); c != 404 {
		t.Fatalf("delete unknown account = %d, want 404", c)
	}

	// carol 的固定 token 删前有效。
	if c := code("GET", "/api/models", "carol-fixed-token"); c != 200 {
		t.Fatalf("carol's token before delete = %d, want 200", c)
	}

	// 删除 carol：200，清单不再有她，token 即刻失效，落盘同步。
	if c := delToken("carol"); c != 200 {
		t.Fatalf("delete carol = %d, want 200", c)
	}
	if c := code("GET", "/api/models", "carol-fixed-token"); c != 401 {
		t.Fatalf("carol's token after delete = %d, want 401", c)
	}
	req, _ := http.NewRequest("GET", ts.URL+"/api/accounts", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	res, err = plain.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if strings.Contains(string(raw), "carol") {
		t.Fatalf("accounts still lists carol: %s", raw)
	}
	disk, err := os.ReadFile(srv.auth.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(disk), "carol") {
		t.Fatalf("users.json still contains carol: %s", disk)
	}

	// 旁证：未牵连的账号照常。
	if c := code("GET", "/api/models", tok); c != 200 {
		t.Fatalf("admin token after delete = %d, want 200", c)
	}

	// 现状交互：开户通道仍在——被删的 carol 凭 admin 凭据可重新自建。
	res, err = plain.Post(ts.URL+"/api/login", "application/json",
		strings.NewReader(`{"username":"carol","password":"pw-carol-2","admin_password":"`+tok+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("carol re-provision = %d, want 200 (provision channel is still open)", res.StatusCode)
	}
}
