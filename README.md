# key-model-router

CLIProxyAPI（CPA）8.0.31 插件：按下游 API Key 把请求固定路由到指定模型，
未配置的 Key 保持 CPA 原生行为（仍按请求里的 `model` 选择 provider）。

适用场景：多个下游 Key 共用一套 CPA，但每个 Key 只想落到某个固定模型。
调用方不需要改请求里的 `model`，也不需要在 CPA 里为每个 Key 维护 provider 分组。

## 工作原理

- 注册 `ModelRouter` 钩子（`sdk/pluginapi/types.go` 的 `RouteModel`）。
- 命中的 Key 返回 `Handled=true`、`TargetKind=self`，交给本插件的 executor。
- executor 只做一件事：把请求体顶层 `model` 改成目标模型，然后调用宿主
  `host.model.execute` / `host.model.execute_stream`。
- 宿主回调会跳过本插件的 router（`SkipRouterPluginID`），因此不会递归；嵌套执行
  重新走 CPA 原生的 model → provider → auth 选择、重试与冷却逻辑，不需要手写
  auth ID，也不需要指定 provider。
- 流式与非流式共用同一条路径；`/v1/chat/completions`、`/v1/responses`、
  `/v1/messages` 均已覆盖。

## 安装

要求 Go 版本与 CPA 主程序一致（本插件在 go1.26 上构建与运行）。

```bash
cd key-model-router
./build.sh                      # 产出 ./key-model-router.so
# 或指定 go 与输出路径
GO=/usr/local/go/bin/go OUT=/tmp/key-model-router.so ./build.sh
```

部署到 CPA（把 `/opt/cpa` 换成实际目录，`cpa:cpa` 换成实际运行用户）：

```bash
install -d -o cpa -g cpa -m 755 /opt/cpa/plugins
install -o cpa -g cpa -m 755 key-model-router.so /opt/cpa/plugins/key-model-router.so

printf '{}\n' > /opt/cpa/key-model-router.json
chown cpa:cpa /opt/cpa/key-model-router.json
chmod 600 /opt/cpa/key-model-router.json
```

生成 UI 登录密码的 bcrypt 哈希（命令只输出哈希，不落明文）：

```bash
go run ./cmd/hashpw 'your-password' > /opt/cpa/key-model-router.pass
chown cpa:cpa /opt/cpa/key-model-router.pass
chmod 600 /opt/cpa/key-model-router.pass
```

在 CPA 的 `config.yaml` 里启用插件，例如：

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    key-model-router:
      enabled: true
      priority: 10
      listen: 127.0.0.1:8320
      cpa_base_url: http://127.0.0.1:8318
      cpa_config_path: /opt/cpa/config.yaml
      state_file: /opt/cpa/key-model-router.json
      password_file: /opt/cpa/key-model-router.pass
```

完整字段见 `config.example.yaml`。改完重启 CPA。

## 配置字段

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `enabled` | `true` | 是否启用按 Key 路由 |
| `listen` | `127.0.0.1:8320` | 设置界面监听地址，只允许 loopback |
| `cpa_base_url` | `http://127.0.0.1:8318` | 本机 CPA 地址，用于读取 `/v1/models` |
| `cpa_config_path` | `/opt/cpa/config.yaml` | 只读取 `api-keys` 用于本机模型探测 |
| `state_file` | `/opt/cpa/key-model-router.json` | 路由状态文件，建议 0600 |
| `password_file` | `/opt/cpa/key-model-router.pass` | bcrypt 哈希文件，必须 0600 |
| `alias_seed_file` | 空 | 可选的指纹 → 别名 JSON，便于界面辨认 Key |
| `model_refresh_seconds` | `300` | 模型列表刷新间隔，最小 30 |
| `session_ttl_seconds` | `28800` | 登录会话有效期，最小 300 |

## 打开设置界面

CPA 8.0.31 的 plugin resource route 只接受 GET，无法承载带登录态的表单 POST。
因此插件在自己的进程内起一个**仅监听 loopback** 的 HTTP 服务，路径与 CPA 资源路径一致：

```
http://127.0.0.1:8320/v0/resource/plugins/key-model-router/
```

通过 SSH 隧道访问（把 `HOST` 换成 CPA 所在主机）：

```bash
ssh -L 18320:127.0.0.1:8320 HOST
# 浏览器打开 http://127.0.0.1:18320/v0/resource/plugins/key-model-router/
```

CPA 自己的端口上不会出现该界面；公网入口也不会暴露它。

## 状态文件

```json
{
  "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef": {
    "alias": "team-a",
    "target_model": "your-target-model"
  }
}
```

- key 为下游 API Key 的 sha256（小写十六进制）。
- 路径由 `state_file` 指定，建议权限 600、属主为 CPA 运行用户。
- 写入方式：同目录临时文件 + fsync + 原子 rename；保存后热加载，不需要重启 CPA。
- 文件损坏时整体按“不路由”处理，请求继续走原生路径，并写 error 日志。
- 目标模型不存在时 executor 返回 400 `model_not_found`，不会回落到原模型；
  界面中对应行会标红。

若已有 Key 指纹与别名的对照表，可用 `scripts/import_cpamp_aliases.py` 生成
`alias_seed_file`（只读数据库，不会写回）。

## 安全模型

- 监听地址只允许 `127.0.0.1` / `localhost` / `::1`，配置非法时插件拒绝启动。
- 密码以 bcrypt 哈希保存，每次登录重新读取，改密无需重启。
- 登录成功下发 `HttpOnly` + `SameSite=Strict` 会话 cookie；会话只存内存。
- 所有写操作只接受 POST，并要求 `X-KMR-CSRF` 与会话中的 CSRF 一致；GET 只读。
- 登录与保存都会校验 `Origin` / `Sec-Fetch-Site`，跨站请求返回 403。
- 登录失败 5 次 / 10 分钟按 IP 限速。
- 日志只记录 Key 的 sha256 前缀、模型名与来源 IP，不记录 Key、密码或 cookie。
- 嵌套调用上游前会移除 `Authorization`、`X-Api-Key`、`Cookie` 等下游凭据头。

## 已知限制

- `count_tokens` 路径不经过 Key 路由，仍按原模型计数，避免把计数请求变成一次
  真实模型调用。
- `/v1/responses` 非流式响应的 `model` 字段可能回显上游 provider 的别名；
  路由本身已生效，chat / messages 路径通常显示目标模型名。
- 模型列表来自本机 `GET /v1/models`，使用 `cpa_config_path` 中前 3 个 `api-keys`
  作为本地探测凭据；不会外发，也不会写入日志。

## 许可

MIT，见 `LICENSE`。
