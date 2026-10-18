# Excel / BPS 协议（最小移植版）

BPS = Basis Points，是 OpenAI 官方 Excel 插件（`bps.openai.com`）的后端 API。本仓库把它作为 **Excel OAuth 账号的按模型路由方式**：通过"BPS OAuth"新建的账号开启 BPS 并圈定模型后，这些模型的 `/v1/responses` 请求经协议适配层改写为 Excel 插件协议发出。协议适配层移植自 ranxi2001/sub2api（其 basispoints 包源自 hloolx/codex2api，MIT）。

## 与 ranxi 原版的差异（本部署裁剪）

- **无 mihomo 会话代理 / Warm IP 池**：BPS 请求始终走账号自身绑定的代理（`proxy_id`）。需要不同出口 IP 时给账号绑不同代理。
- **无自动重登（reauth/2FA worker）与双凭据表**：只有"BPS OAuth（官方 Excel 登录）"创建的 Excel-only 账号。RT 失效后由管理员重新走一遍手动授权流。
- **普通 Codex OAuth 账号不能开 BPS**：BPS 只接受官方 Excel 客户端（`client_id = app_fnr0pYvVwwFDocDumLG3H2Bp`）签发的 token。ranxi 会为 Codex 账号另外申请一份独立的 Excel 授权，本构建没有移植这部分。因此：
  - BPS 开关只对 Excel OAuth 账号生效，编辑页和批量编辑页只对这类账号显示 BPS 设置；
  - 后端对非 Excel OAuth 账号开启 `openai_excel_bps` 的保存一律返回 400 `OPENAI_EXCEL_BPS_INVALID`；
  - 已经带有 `openai_excel_bps` 旧标记的 Codex 账号会忽略该标记，继续走原生 Codex，下次在编辑页保存时会清掉这些旧键。
- **无图片输入支持**：带图片输入（`input_image`）的请求返回 400 `basispoints_request_invalid`（"not supported in this build"）。图片生成工具同理。
- **无 account_quality 联动 / 自动配置模板 / 定时探针 / 请求采集**。

## 建号流程（手动 OAuth）

1. 管理后台 → 添加账号 → OpenAI → **BPS OAuth**，勾选模型白名单（默认 gpt-6-astra / gpt-5.6-sol / gpt-5.6-terra）。
2. 生成授权链接（后端用 Excel 专用 client `app_fnr0pYvVwwFDocDumLG3H2Bp` 走 PKCE，state 前缀 `bps.`）。
3. 浏览器打开链接完成 OpenAI 登录授权；无论页面是否提示错误，复制 `bps.openai.com` 地址栏里含 `code` 与 `state` 的完整回调 URL。
4. 粘贴回管理页兑换，系统校验返回的 `client_id` 必须是 Excel client，创建账号（凭据存 `accounts.credentials`，AT 过期按 credentials 里的 client_id 自动刷新）。

已知限制：Excel-only 账号**没有原生 Codex 通道**——非白名单模型的请求返回 400 `excel_oauth_route_unavailable`；调度器也不会把该账号派给非白名单模型。托管工具（web_search 等）回退原生不可用，需开启"省略不支持的托管工具"。

## 账号级配置（extra 键）

| 键 | 说明 |
|---|---|
| `openai_excel_bps` | 协议开关（仅 Excel OAuth 账号生效；free 套餐账号禁止开启） |
| `openai_excel_bps_models` | 模型白名单（按账号映射后的模型名匹配）；键不存在=所有模型 |
| `openai_excel_bps_omit_unsupported_tools` | 保持 BPS 并省略不支持的托管工具 |
| `openai_excel_bps_ignore_encrypted_content` | 旧会话密文替换为省略提示 |
| `openai_excel_bps_cache_creation_as_input` | 缓存创建 token 按普通输入计费 |
| `openai_excel_bps_auto_disable_on_403` | 403 自动关闭协议 |
| `openai_excel_bps_auto_recover_on_403` + `..._403_recovery_interval_minutes` | 关闭后按间隔（1–10080 分钟，默认 60）后台探测恢复 |
| `openai_excel_bps_auto_move_on_403` + `..._403_target_group_id` | 403 自动移组（0=退出全部分组） |

## 全局开关与配置

- 设置页（gateway 分区）`excel_bps_enabled`：DB 键，默认 true；关闭后所有 BPS 路由停用，Excel-only 账号变为不可调度。
- `config.yaml` `gateway.excel_bps_stream_data_interval_timeout`：流无数据帧超时（秒，0 禁用，30–300，默认 120）。
- `config.yaml` `gateway.excel_bps_state_redis_enabled`：工具回放/目录状态共享 Redis（多实例）；默认 false（进程内）。

## 行为要点

- **调度**：白名单模型优先派给 BPS 账号；BPS 429 只冷却该账号的 BPS 路由（`excel_bps_rate_limited` 排除原因）并允许换号重试；模型权限 403 冷却该（账号,模型）（`excel_bps_model_not_allowed`）。
- **传输**：BPS 走独立 HTTP 上游 profile（`excel_bps`），经 HTTP 代理出现 H2 流错误时该代理短时熔断降级 HTTP/1.1（1 分钟试探，独立连接池）。
- **计费**：`/basispoints/api/responses` 端点按 Responses 计费；`cache_creation_as_input` 打开后缓存创建并入普通输入。
- **模型发现**：`GET /basispoints/api/responses/access?include_models=true` 实时读取 allowed/restricted/efforts，60 秒缓存 + singleflight；模型列表与 Codex manifest 会按白名单裁剪合并。
- **WS**：BPS 模型拒绝 WebSocket 接入（要求 HTTP/SSE）。
- **账号测试**：BPS 账号的默认测试走与用户请求相同的网关转发路径；另可选"BPS 工具往返"（nonce → echo 工具 → 回传核对，最多 3 次上游请求）。BPS 拒绝请求时，测试弹窗会显示上游 HTTP 状态码、Request ID、错误码和脱敏后的错误消息，错误行末尾附 `(HTTP <状态码>)`。
- **403 处置**：上游 403（非模型权限错误）时按账号配置自动关闭协议、自动移组；开启自动恢复后由后台循环（随网关服务自启）小额探测并自动恢复。

## 升级与上游

`backend/internal/service/basispoints/` 为自包含协议包（仅 stdlib + jsonschema + gjson）。bps.openai.com 协议无 SLA；如需跟进协议变化，可对照 ranxi2001/sub2api 或 hloolx/codex2api 的更新做增量移植——包内无本仓库其他依赖，整目录替换的冲突面很小。

## 验证清单（手动 E2E）

1. BPS OAuth 建号 → 账号测试（BPS 工具往返）通过。
2. 白名单模型 `/v1/responses`（流式/非流式）经 `basispoints` 正常返回，用量行 `upstream_endpoint=/basispoints/api/responses`。
3. 非白名单模型请求 → 400 `excel_oauth_route_unavailable`。
4. 关闭全局开关 → 请求不再走 BPS。
5. （可选）制造 429/403 观察冷却与自动关闭/恢复日志（`excel_bps.*`）。
