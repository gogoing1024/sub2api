# Excel / BPS 协议（最小移植版）

BPS = Basis Points，是 OpenAI 官方 Excel 插件（`bps.openai.com`）的后端 API。本仓库把它作为独立的账号类型 **BPS OAuth（`bps-oauth`）**：通过官方 Excel 客户端授权的 OpenAI 账号，按模型白名单把 `/v1/responses` 请求经协议适配层改写为 Excel 插件协议发出。协议适配层移植自 ranxi2001/sub2api（其 basispoints 包源自 hloolx/codex2api，MIT）。

## 与 ranxi 原版的差异（本部署裁剪）

- **BPS-only 账号类型**：BPS 只接受官方 Excel 客户端（`client_id = app_fnr0pYvVwwFDocDumLG3H2Bp`）签发的 token。ranxi 会为普通 Codex 账号另外申请一份独立的 Excel 授权（双凭据 + 自动重登 worker），本构建没有移植这部分，因此：
  - BPS 只属于 `bps-oauth` 账号，没有账号级开关；普通 Codex OAuth（`oauth`）账号不能走 BPS。
  - 账号类型与凭据必须一致：`bps-oauth` 必须持有 Excel client 的凭据，`oauth` 账号不能持有 Excel client 的凭据，否则保存返回 400 `BPS_OAUTH_INVALID`。
- **无 mihomo 会话代理 / Warm IP 池**：BPS 请求始终走账号自身绑定的代理（`proxy_id`）。需要不同出口 IP 时给账号绑不同代理。
- **无自动重登（reauth/2FA worker）**：RT 失效后由管理员在账号菜单里"重新授权"（走 Excel 客户端）。
- **无 403 自动关闭 / 自动恢复 / 自动移组**：对 BPS-only 账号来说，关闭协议或移组都等于整号不可用，所以普通 BPS 403 直接交给账号级处理（见下文"行为要点"）。
- **无图片输入支持**：带图片输入（`input_image`）的请求返回 400 `basispoints_request_invalid`（"not supported in this build"）。图片生成工具同理。
- **无 account_quality 联动 / 自动配置模板 / 定时探针 / 请求采集**。

## 建号流程（手动 OAuth）

1. 管理后台 → 添加账号 → OpenAI → **BPS OAuth**，勾选模型白名单（默认 gpt-6-astra / gpt-5.6-sol / gpt-5.6-terra）。
2. 生成授权链接（后端用 Excel 专用 client 走 PKCE，state 前缀 `bps.`）。
3. 浏览器打开链接完成 OpenAI 登录授权；无论页面是否提示错误，复制 `bps.openai.com` 地址栏里含 `code` 与 `state` 的完整回调 URL。
4. 粘贴回管理页兑换，系统校验返回的 `client_id` 必须是 Excel client，创建 `bps-oauth` 账号（凭据存 `accounts.credentials`，AT 过期按 credentials 里的 client_id 自动刷新）。也可以直接粘贴 Excel 客户端的 Refresh Token。

## 能力与限制

- **只服务 BPS**：没有原生 Codex 通道。白名单外的模型请求返回 400 `excel_oauth_route_unavailable`，调度器也不会把该账号派给白名单外的模型。
- **只支持 Responses 接口**：`/v1/chat/completions`、`/v1/messages`、图片接口都不会选中 BPS OAuth 账号。想同时提供 BPS 与非 BPS 模型或接口，把 BPS OAuth 账号和普通 Codex OAuth 账号放在同一个分组里，调度会按模型和入口分流。
- **只走 HTTP/SSE**：不接受 WebSocket 会话。
- **托管工具**：联网搜索、图片生成等托管工具会被适配层省略，并告知模型能力不可用；客户端函数工具不受影响。
- **不设置 privacy**：ChatGPT 的 privacy 设置属于 Codex 账号逻辑，要求设置 privacy 的分组不会选中 BPS OAuth 账号。
- **Codex 专属功能不适用**：打票、Codex 指纹、WS v2、compact、额度重置（wham）、Spark 影子等都不作用于 BPS OAuth 账号。

## 账号级配置（extra 键）

| 键 | 说明 |
|---|---|
| `openai_excel_bps_models` | 模型白名单（按账号映射后的模型名匹配）；键不存在 = BPS 可用的所有模型 |
| `openai_excel_bps_ignore_encrypted_content` | 旧会话里 BPS 无法转发的密文替换为省略提示 |
| `openai_excel_bps_cache_creation_as_input` | 缓存创建 token 按普通输入计费 |

另外沿用通用的长上下文计费与 5h/7d 自动暂停设置。free 套餐账号不能走 BPS。

## 全局开关与配置

- 设置页（gateway 分区）`excel_bps_enabled`：DB 键，默认 true；关闭后所有 BPS 路由停用，BPS OAuth 账号变为不可调度。
- `config.yaml` `gateway.excel_bps_stream_data_interval_timeout`：流无数据帧超时（秒，0 禁用，30–300，默认 120）。
- `config.yaml` `gateway.excel_bps_state_redis_enabled`：工具回放/目录状态共享 Redis（多实例）；默认 false（进程内）。

## 行为要点

- **调度**：白名单模型优先派给 BPS 账号；BPS 429 只冷却该账号的 BPS 路由（`excel_bps_rate_limited` 排除原因）并允许换号重试；模型权限 403 冷却该（账号,模型）（`excel_bps_model_not_allowed`）。
- **普通 401/403**：BPS-only 账号没有别的通道，按整号处理并换号重试：
  - 401：清 token 缓存并临时停调度，给 token 刷新留窗口；token 已吊销等情况标记错误。
  - 403：交给 OpenAI 通用 403 处理——HTML / Cloudflare 拦截页不处罚，其余先临时冷却，累计多次才永久停用。
  - 两者都返回固定文案，不透出 BPS 原文；流式修复请求已开始输出时只做整号处理，不换号。
- **token 生命周期**：取 token、后台刷新、手动刷新、重新授权都按 OpenAI OAuth 处理，刷新使用凭据里的 Excel client_id。
- **用量**：5h/7d 窗口来自 BPS 响应头快照与用量日志，不会向 chatgpt.com 发起 Codex 探测。
- **传输**：BPS 走独立 HTTP 上游 profile（`excel_bps`），经 HTTP 代理出现 H2 流错误时该代理短时熔断降级 HTTP/1.1（1 分钟试探，独立连接池）。
- **计费**：`/basispoints/api/responses` 端点按 Responses 计费；`cache_creation_as_input` 打开后缓存创建并入普通输入。
- **模型发现**：`GET /basispoints/api/responses/access?include_models=true` 实时读取 allowed/restricted/efforts，60 秒缓存 + singleflight；模型列表与 Codex manifest 按白名单裁剪。
- **账号测试**：默认测试走与用户请求相同的网关转发路径；另可选"BPS 工具往返"（nonce → echo 工具 → 回传核对，最多 3 次上游请求）。BPS 拒绝请求时，测试弹窗显示上游 HTTP 状态码、Request ID、错误码和脱敏后的错误消息，错误行末尾附 `(HTTP <状态码>)`。

## 升级与上游

`backend/internal/service/basispoints/` 为自包含协议包（仅 stdlib + jsonschema + gjson）。bps.openai.com 协议无 SLA；如需跟进协议变化，可对照 ranxi2001/sub2api 或 hloolx/codex2api 的更新做增量移植——包内无本仓库其他依赖，整目录替换的冲突面很小。

## 验证清单（手动 E2E）

1. BPS OAuth 建号，账号类型显示为 "BPS OAuth"；账号测试（默认与 BPS 工具往返）通过。
2. 白名单模型 `/v1/responses`（流式/非流式）经 `basispoints` 正常返回，用量行 `upstream_endpoint=/basispoints/api/responses`。
3. 白名单外模型请求 → 400 `excel_oauth_route_unavailable`；`/v1/chat/completions` 不会派到 BPS OAuth 账号。
4. 关闭全局开关 → BPS OAuth 账号不再被调度。
5. （可选）制造 BPS 403（例如换一个被拒的代理）→ 账号临时停调度，请求换到同分组的其他账号。
