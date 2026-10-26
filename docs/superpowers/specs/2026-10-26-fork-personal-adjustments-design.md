# 自用分支调整设计

## 背景

本仓库是 octopus 的自用分支，`dev` 分支用于承载不向上游提交的个性化调整。本次共四项：

1. 新建/编辑渠道拉取模型后，默认协议由 Responses 改为 Chat Completions。
2. 恢复历史版本中的定时更新渠道模型功能，并恢复设置页的手动同步入口。
3. 首页趋势切换今天/7 天/30 天时，下方渠道榜和模型榜的金额、次数、词元同步联动。
4. 日志详情保留并展示一次请求中每一轮故障转移的失败原因。

版本号以上游 `main.go` 中的 `// Version` 为准（当前 `v0.13.13`），自用调整不自增版本号。最终构建 `linux/amd64` 与 `linux/arm64` 镜像并推送阿里云仓库。

## 第 1 项：默认协议改为 Chat

### 行为

- 拉取模型时，OpenAI 侧探测到的模型记为 `ProtocolOpenAIChatCompletion`，不再记为 `ProtocolOpenAIResponse`。
- Anthropic 侧探测到的模型仍记为 `ProtocolAnthropicMessage`。
- 手动输入模型名添加时，如果当前凭据没有任何授权，兜底协议由 `OpenAIResponse` 改为 `OpenAIChatCompletion`；凭据已有授权时仍沿用已有协议的并集。

### 实现

- 修改 `internal/server/handlers/channel.go` 中 `fetchModel` 的协议位赋值。
- 修改 `web/src/components/modules/channel/FormGrants.tsx` 中 `addModel` 的兜底协议。
- 同步更新相关注释，避免继续描述“默认 Responses”。

## 第 2 项：恢复定时更新渠道模型

### 数据模型

- `ChannelConfig` 增加 `AutoSync bool`（JSON `auto_sync`，默认 `false`），随渠道详情接口读写。
- `ChannelModel` 增加内部字段 `Source`（`manual` / `auto`，默认 `manual`），不暴露给前端。
- 新增迁移 `013`：
  - `channels.auto_sync`，默认 `false`。
  - `channel_models.source`，默认 `manual`。
- `ChannelUpdate` 显式更新列加入 `auto_sync`；表单新建的模型写 `manual`，已存在模型保留原 `source`。
- 恢复 `SettingKeySyncLLMInterval`（`sync_llm_interval`，默认 `24` 小时），整数校验；`0` 表示停止定时任务。

### 探测复用

- 将 `fetchOpenAIModels`、`fetchAnthropicModels`、`modelsURL` 与正则过滤抽取到 `internal/helper/models.go`。
- 表单探测、定时任务、手动同步共用同一份探测逻辑。
- OpenAI 侧返回的协议位按第 1 项记为 Chat。

### 同步语义

- 只处理 `auto_sync = true` 的渠道，不要求渠道当前启用。
- 每把启用的 Key 分别探测；探测失败的 Key 只记录日志，不修改其任何授权。
- 模型集合：
  - 手动模型永久保留，同步不修改其授权。
  - 自动模型取所有探测成功 Key 的并集；上游不再返回的自动模型连同授权删除。
  - 新模型标记为 `auto`，并补价格表记录；全部渠道处理完后清理幽灵价格。
- 对探测成功的 Key，只替换自动模型的授权协议位；该 Key 不再返回的自动模型删除对应授权。
- 单个渠道失败不影响其他渠道；每个渠道的模型与授权变更在同一事务内完成，随后刷新缓存与分组。
- 定时任务使用 `TryLock` 防止重叠执行，30 分钟超时；启动时立即执行一次。
- 同步结束更新时间戳；手动同步与定时同步共用同一把锁。

### 新增操作

- `op.ChannelModelsSync`：按渠道执行自动模型与授权的增量同步，保持手动模型和失败 Key 的授权不变。
- `task.SyncModelsTask`：遍历满足条件的渠道并调用上述操作。
- `task.GetLastSyncModelsTime`：返回最近一次同步结束时间。

### API 与界面

- 恢复 `POST /api/v1/channel/sync`：手动同步；正在执行时返回“同步进行中”。
- 恢复 `GET /api/v1/channel/last-sync-time`。
- 设置页恢复“LLM 同步”卡片：同步间隔、立即同步按钮、最近同步时间。
- 渠道表单“高级”步骤增加“自动同步”开关。
- 补齐 `zh_hans`、`zh_hant`、`en` 翻译。

### 不包含

- 不恢复自动分组功能。当前代码中分组匹配逻辑已整体移除，需要单独设计。

## 第 3 项：首页榜单周期联动

### 数据模型

新增按天统计表：

- `channel_stats_daily`：`channel_id` + `date` 复合主键，内嵌 `StatsMetrics`。
- `channel_model_stats_daily`：`channel_model_id` + `date` 复合主键，内嵌 `StatsMetrics`。

统计口径与现有渠道/模型累计统计一致：

- 失败轮次：`RequestFailed = 1`，记录等待耗时。
- 非流式成功：记录 Token、费用、等待耗时，`RequestSuccess = 1`。
- 流式成功/失败：流结束时记录 Token、费用、等待耗时与对应成功/失败计数。

### 写入与持久化

- 在 relay 现有 `ChannelStatsUpdate` / `ChannelModelStatsUpdate` 调用点同步累加当天内存数据。
- 今天的按天数据由现有 `StatsSaveDB` 周期任务落库；跨天时先写前一天再重置。
- 保留 90 天；渠道或模型删除时同步删除其按天历史。

### 查询

`GET /api/v1/channel/stats` 增加可选 `period`：

- 不带 `period`：返回现有全量累计统计，渠道列表页行为不变。
- `period=1|7|30`：汇总对应日期范围（今天、今天+前 6 天、今天+前 29 天），并入今天尚未落库的内存数据。
- 周期查询隐藏请求数为 0 的渠道和模型。

### 前端

- `useChannelStats(period)` 将 period 纳入 queryKey。
- 首页 `Rank` 复用 `useHomeViewStore` 的 `chartPeriod`，渠道榜和模型榜随周期切换重新取数。
- 渠道列表页继续使用无 period 的累计接口。

## 第 4 项：日志保留每轮失败原因

### 后端

- `RequestState` 增加 `Rounds []RoundRecord`，每轮记录：
  - `round`、`round_started_at`
  - `target_channel_key`、`target_model`、`target_protocol`
  - `sending`、`error`
- 最多保留 20 轮；每次写入采用写时复制，避免 SSE 并发读写竞争。
- `startRound` 追加记录并置 `sending = true`、`error = ""`。
- `finishRound` 更新最后一轮的 `sending` 与错误文本。
- 保留现有 `round`、`error`、`sending` 字段，兼容日志卡片实时状态。

### 前端

- `RelayLogOverview` 增加 `rounds` 类型。
- 日志详情弹窗直接渲染后端轮次历史，删除当前靠截获瞬时 SSE 快照拼接轮次的逻辑。
- 后续模型请求中或请求失败时，可看到此前每一轮的渠道/Key、开始时间和错误。
- 刷新页面或重连 SSE 后历史仍在。
- 卡片保留最新错误，并显示“已重试 N 次”。

## 测试与验证

- 后端：`go test ./...`、`go vet ./...`。
- 为模型合并/删除、协议合并、手动/自动分离、轮次历史上限等纯逻辑补单元测试。
- 前端：`pnpm build`（含 `tsc --noEmit`）。
- 端到端验证：
  - 拉取模型默认勾选 Chat，Anthropic 侧仍为 Message。
  - 定时/手动同步覆盖新增模型、协议变化、模型下线、Key 失败四种情况。
  - 首页切换 1/7/30 天时榜单数据变化，零数据项隐藏。
  - 故障转移请求在前一模型失败、后一模型请求中时，详情能看到前序失败原因。

## 交付

- 版本号沿用上游 `v0.13.13`。
- 构建 `linux/amd64`、`linux/arm64` 多架构镜像，推送：
  - `registry.cn-hangzhou.aliyuncs.com/ulquiola/octopus:v0.13.13`
  - `registry.cn-hangzhou.aliyuncs.com/ulquiola/octopus:latest`
- buildx 需带 `--provenance=false --sbom=false`，否则阿里云 ACR 拒绝 attestation manifest。