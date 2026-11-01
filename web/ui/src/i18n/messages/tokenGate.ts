// TokenGate.vue: the page that takes over the viewport when the API answers
// 401 — token absent or rejected.
export default {
  namespace: "tokenGate",
  messages: {
    "zh-CN": {
      title: "缺少访问令牌",
      subtitle: "这个实例开启了令牌校验，页面需要 token 才能调用 API。在下面粘贴令牌即可进入。",
      rejected: "刚才带的令牌被服务器拒绝了，请换成本次启动打印的那个。",
      hintStartup: "令牌在服务启动时打印的地址里，即 {url} 后面的部分",
      hintDocker: "容器部署时把地址换成宿主机端口，例如 {url2}",
      placeholder: "粘贴令牌",
      enter: "进入",
      usePassword: "使用账号密码登录",
      useToken: "使用令牌进入",
    },
    en: {
      title: "Access token required",
      subtitle: "This instance checks a shared token; the page needs one before the API will answer. Paste it below to continue.",
      rejected: "The token presented was rejected by the server — use the one printed by this launch.",
      hintStartup: "The token is in the address printed at startup, after {url}",
      hintDocker: "Behind Docker, swap in the host port, e.g. {url2}",
      placeholder: "Paste the token",
      enter: "Enter",
      usePassword: "Sign in with username and password",
      useToken: "Use an access token",
    },
  },
};
