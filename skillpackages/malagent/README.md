# MalAgent Skill

标准 Skill 源目录：`SKILL.md` + `references/`。所有分析工具属于同一个 Skill。

发布者使用 `cmd/pogo-malagent-package` 将工具、规则和索引一起打包为 Skill ZIP。
必须提供 `-sample-index`，不再生成外部数据绑定模板，也没有安装脚本。
生成的 ZIP 根目录包含 `SKILL.md`、`pogo-skill.json`、`references/`、`contracts/`、`payload/`。
`pogo-skill.json` 是 Pogo 可选运行扩展，声明协议为 `pogo.skill/v1` / `SkillRuntime`。

管理员在 Studio 的 Skill 管理上传 ZIP，创建 Agent 时只勾选 MalAgent。
不接受原来的发行包格式；不能只改 ZIP 文件名，须使用新版打包器重新构建。
