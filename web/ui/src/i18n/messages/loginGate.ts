export default {
  namespace: "loginGate",
  messages: {
    "zh-CN": {
      title: "登录",
      subtitle: "本部署启用了账号密码登录。会话有效期内不需要 token。",
      username: "账号",
      password: "密码",
      enter: "登录",
      busy: "登录中…",
      rejected: "账号或密码错误",
      locked: "失败次数过多，请 15 分钟后再试",
    },
    en: {
      title: "Sign in",
      subtitle: "This deployment uses username and password. No token is needed while the session lasts.",
      username: "Username",
      password: "Password",
      enter: "Sign in",
      busy: "Signing in…",
      rejected: "Wrong username or password",
      locked: "Too many failed attempts; retry in 15 minutes",
    },
  },
} as const;
