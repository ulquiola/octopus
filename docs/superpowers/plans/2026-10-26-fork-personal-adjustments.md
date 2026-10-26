# 自用分支调整实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 `dev` 分支实现四项自用调整，并构建 linux/amd64 + linux/arm64 镜像推送到阿里云。

**Architecture:** 第 1、4 项是现有流程的局部修改；第 2 项按当前“多凭据 + 模型授权”模型重建定时同步（独立同步事务 + 渠道级开关）；第 3 项新增渠道/模型按天统计表，并由现有统计任务持久化。

**Tech Stack:** Go 1.26（gin、gorm）、React 19 + TypeScript + Vite、Docker buildx。

**Spec:** `docs/superpowers/specs/2026-10-26-fork-personal-adjustments-design.md`

## Global Constraints

- 版本号以上游 `main.go` 的 `// Version` 为准，当前为 `v0.13.13`，本次不自增。
- 所有新增注释使用中文，风格与现有代码一致。
- 协议位常量不可改值：Chat=`1<<1`、Response=`1<<2`、Message=`1<<3`。
- 镜像仓库：`registry.cn-hangzhou.aliyuncs.com/ulquiola/octopus`。
- buildx 必须带 `--provenance=false --sbom=false`，否则阿里云 ACR 拒绝 attestation manifest。
- 不恢复自动分组功能。

---

### Task 1: 默认协议改为 Chat

**Files:**
- Modify: `internal/server/handlers/channel.go:306`
- Modify: `web/src/components/modules/channel/FormGrants.tsx:116-128`
- Modify: `web/src/api/channel.ts:122-127`

**Interfaces:**
- Consumes: `model.ProtocolOpenAIChatCompletion`
- Produces: 探测与手动添加的默认协议均为 Chat。

- [ ] **Step 1: 修改服务端协议位**

把 `internal/server/handlers/channel.go` 中 OpenAI 侧合并处：

```go
protocolsByModel[name] |= model.ProtocolOpenAIResponse
```

改为：

```go
protocolsByModel[name] |= model.ProtocolOpenAIChatCompletion
```

同时把函数上方注释里的“OpenAI 侧记为 Responses”改为“OpenAI 侧记为 Chat”。

- [ ] **Step 2: 修改手动添加的兜底协议**

`web/src/components/modules/channel/FormGrants.tsx` 中：

```ts
if (activeKey) grants.set(grantKey(name, activeKey), protocols || Protocol.OpenAIResponse);
```

改为：

```ts
if (activeKey) grants.set(grantKey(name, activeKey), protocols || Protocol.OpenAIChatCompletion);
```

并把上方注释中的“退回 Responses”改为“退回 Chat”。

- [ ] **Step 3: 更新前端类型注释**

`web/src/api/channel.ts` 中 `FetchModel` 注释：

```ts
// OpenAI 侧记为 Responses 而非 Chat：Chat Completions 已被官方标记弃用，需要 Chat 的渠道由用户手动勾选。
```

改为：

```ts
// OpenAI 侧记为 Chat：自用分支默认走 Chat Completions，需要 Responses 的渠道由用户手动勾选。
```

- [ ] **Step 4: 验证**

Run: `go build ./...`

Expected: 无输出，退出码 0。

- [ ] **Step 5: 提交**

```bash
git add internal/server/handlers/channel.go web/src/components/modules/channel/FormGrants.tsx web/src/api/channel.ts
git commit -m "feat: :sparkles: 拉取模型默认协议改为 Chat"
```

---

### Task 2: 渠道自动同步字段与迁移

**Files:**
- Modify: `internal/model/channel.go`
- Create: `internal/db/migrate/013.go`
- Modify: `internal/op/channel.go:104-110`, `internal/op/channel.go:563-598`

**Interfaces:**
- Produces:
  - `model.ChannelConfig.AutoSync bool`
  - `model.ChannelModel.Source model.ChannelModelSource`
  - `model.ChannelModelSourceManual`、`model.ChannelModelSourceAuto`
  - `ChannelDetail` 的 JSON 自动包含 `auto_sync`。

- [ ] **Step 1: 增加模型字段**

在 `internal/model/channel.go` 的 `ChannelConfig` 中增加：

```go
AutoSync                 bool           `json:"auto_sync" gorm:"default:false"` // 是否参与定时同步上游模型。
```

在 `ChannelModel` 前增加类型与常量：

```go
type ChannelModelSource string

const (
	ChannelModelSourceManual ChannelModelSource = "manual" // 人工添加或编辑表单保留的模型。
	ChannelModelSourceAuto   ChannelModelSource = "auto"   // 由定时同步拉取并维护的模型。
)
```

在 `ChannelModel` 中增加：

```go
Source       ChannelModelSource `json:"-" gorm:"not null;default:manual"` // 模型来源; 自动模型由同步任务维护。
```

- [ ] **Step 2: 新增迁移 013**

创建 `internal/db/migrate/013.go`：

```go
package migrate

import (
	"fmt"

	"github.com/bestruirui/octopus/internal/model"
	"gorm.io/gorm"
)

func init() {
	RegisterBeforeAutoMigration(Migration{
		Version: 13,
		Up:      migrateChannelAutoSync,
	})
}

// migrateChannelAutoSync 为渠道增加自动同步开关, 为渠道模型增加来源标记。
// 旧数据按关闭自动同步、手动模型处理; 两列都由 GORM 生成 DDL, 避免各数据库方言的默认值写法差异。
func migrateChannelAutoSync(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}
	if db.Migrator().HasTable("channels") && !hasPhysicalColumn(db, "channels", "auto_sync") {
		if err := db.Migrator().AddColumn(&model.Channel{}, "AutoSync"); err != nil {
			return fmt.Errorf("failed to add channels.auto_sync: %w", err)
		}
		if err := db.Table("channels").Where("auto_sync IS NULL").Update("auto_sync", false).Error; err != nil {
			return fmt.Errorf("failed to backfill channels.auto_sync: %w", err)
		}
	}
	if db.Migrator().HasTable("channel_models") && !hasPhysicalColumn(db, "channel_models", "source") {
		if err := db.Migrator().AddColumn(&model.ChannelModel{}, "Source"); err != nil {
			return fmt.Errorf("failed to add channel_models.source: %w", err)
		}
		if err := db.Table("channel_models").
			Where("source IS NULL OR source = ''").
			Update("source", string(model.ChannelModelSourceManual)).Error; err != nil {
			return fmt.Errorf("failed to backfill channel_models.source: %w", err)
		}
	}
	return nil
}
```

- [ ] **Step 3: 更新时写入 auto_sync，新建模型写 manual**

`internal/op/channel.go` 的 `ChannelUpdate` 显式列中加入 `auto_sync`：

```go
Select("name", "dialect", "enabled", "base_url",
	"openai_chat_completion_path", "openai_response_path", "anthropic_message_path",
	"proxy", "channel_proxy", "custom_header", "param_override", "match_regex", "auto_sync").
```

`syncChannelModels` 创建模型处：

```go
if err := tx.Create(&model.ChannelModel{
	ChannelID: channelID,
	Name:      requestedModel,
	Source:    model.ChannelModelSourceManual,
}).Error; err != nil {
```

- [ ] **Step 4: 验证**

Run: `go build ./...`

Expected: 无输出，退出码 0。

- [ ] **Step 5: 提交**

```bash
git add internal/model/channel.go internal/db/migrate/013.go internal/op/channel.go
git commit -m "feat: :sparkles: 渠道增加自动同步与模型来源字段"
```

---

### Task 3: 抽取模型探测逻辑

**Files:**
- Create: `internal/helper/models.go`
- Create: `internal/helper/models_test.go`
- Modify: `internal/server/handlers/channel.go`（删除探测实现，改为调用 helper）

**Interfaces:**
- Produces:
  - `helper.FetchModels(ctx context.Context, config model.ChannelConfig, key string) ([]model.ChannelFetchModel, error)`
  - `helper.ModelsURL(baseURL, protocolPath string) string`

- [ ] **Step 1: 先写失败测试**

创建 `internal/helper/models_test.go`：

```go
package helper

import "testing"

func TestModelsURL(t *testing.T) {
	cases := []struct{ base, path, want string }{
		{"https://api.openai.com", "/v1/chat/completions", "https://api.openai.com/v1/models"},
		{"https://api.openai.com", "/v1/responses", "https://api.openai.com/v1/models"},
		{"https://api.anthropic.com", "/v1/messages", "https://api.anthropic.com/v1/models"},
	}
	for _, tc := range cases {
		if got := modelsURL(tc.base, tc.path); got != tc.want {
			t.Fatalf("modelsURL(%q, %q) = %q, want %q", tc.base, tc.path, got, tc.want)
		}
	}
}
```

Run: `go test ./internal/helper`

Expected: FAIL，包不存在或 `modelsURL` 未定义。

- [ ] **Step 2: 实现 helper**

创建 `internal/helper/models.go`，内容为从 `internal/server/handlers/channel.go` 迁移过来的逻辑：

```go
package helper

import (
	"context"
	"fmt"
	"net/http"
	"path"
	"strings"
	"sync"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/rhttp"
	"github.com/dlclark/regexp2"
)

// FetchModels 按渠道配置与凭据同时探测 OpenAI 与 Anthropic 两侧的模型列表。
// OpenAI 侧记为 Chat, Anthropic 侧记为 Message; 两侧都失败才返回错误。
func FetchModels(ctx context.Context, config model.ChannelConfig, key string) ([]model.ChannelFetchModel, error) {
	client, closeIdle, err := newModelHTTPClient(config)
	if err != nil {
		return nil, err
	}
	if closeIdle != nil {
		defer closeIdle()
	}

	var openaiModels, anthropicModels []string
	var openaiErr, anthropicErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		openaiModels, openaiErr = fetchOpenAIModels(client, ctx, config, key, modelsURL(config.BaseURL, config.OpenAIChatCompletionPath))
	}()
	go func() {
		defer wg.Done()
		anthropicModels, anthropicErr = fetchAnthropicModels(client, ctx, config, key, modelsURL(config.BaseURL, config.AnthropicMessagePath))
	}()
	wg.Wait()

	if openaiErr != nil && anthropicErr != nil {
		return nil, fmt.Errorf("openai: %v; anthropic: %v", openaiErr, anthropicErr)
	}

	re, reGlobal, err := compileModelFilters(config.MatchRegex)
	if err != nil {
		return nil, err
	}
	protocolsByModel := make(map[string]model.Protocol, len(openaiModels)+len(anthropicModels))
	order := make([]string, 0, len(openaiModels)+len(anthropicModels))
	add := func(names []string, protocol model.Protocol) error {
		for _, name := range names {
			matched, err := matchesModel(name, re, reGlobal)
			if err != nil {
				return err
			}
			if !matched {
				continue
			}
			if _, ok := protocolsByModel[name]; !ok {
				order = append(order, name)
			}
			protocolsByModel[name] |= protocol
		}
		return nil
	}
	if err := add(openaiModels, model.ProtocolOpenAIChatCompletion); err != nil {
		return nil, err
	}
	if err := add(anthropicModels, model.ProtocolAnthropicMessage); err != nil {
		return nil, err
	}
	models := make([]model.ChannelFetchModel, 0, len(order))
	for _, name := range order {
		models = append(models, model.ChannelFetchModel{Name: name, Protocols: protocolsByModel[name]})
	}
	return models, nil
}

func newModelHTTPClient(config model.ChannelConfig) (*http.Client, func(), error) {
	switch {
	case !config.Proxy:
		client, err := rhttp.Direct()
		return client, nil, err
	case config.ChannelProxy == "":
		client, err := rhttp.Proxy()
		return client, nil, err
	default:
		client, err := rhttp.New(config.ChannelProxy)
		if err != nil || client == nil {
			return client, nil, err
		}
		return client, client.CloseIdleConnections, nil
	}
}

func compileModelFilters(matchRegex string) (*regexp2.Regexp, *regexp2.Regexp, error) {
	var re *regexp2.Regexp
	var err error
	if matchRegex != "" {
		if re, err = regexp2.Compile(matchRegex, regexp2.ECMAScript); err != nil {
			return nil, nil, err
		}
	}
	// 全局过滤由设置页维护, 读取失败按不过滤处理。
	globalFilter, _ := op.SettingGetString(model.SettingKeyModelFilter)
	var reGlobal *regexp2.Regexp
	if globalFilter != "" {
		if reGlobal, err = regexp2.Compile(globalFilter, regexp2.ECMAScript); err != nil {
			return nil, nil, err
		}
	}
	return re, reGlobal, nil
}

func matchesModel(name string, re, reGlobal *regexp2.Regexp) (bool, error) {
	if re != nil {
		matched, err := re.MatchString(name)
		if err != nil || !matched {
			return false, err
		}
	}
	if reGlobal != nil {
		matched, err := reGlobal.MatchString(name)
		if err != nil || !matched {
			return false, err
		}
	}
	return true, nil
}

// modelsURL 取协议请求路径的父级目录, 与地址拼成同级的 /models 地址。
func modelsURL(baseURL, protocolPath string) string {
	parent := path.Dir(strings.TrimRight(protocolPath, "/"))
	if strings.HasSuffix(parent, "/chat") {
		parent = path.Dir(parent)
	}
	if parent == "." || parent == "/" {
		parent = ""
	}
	return strings.TrimRight(baseURL, "/") + parent + "/models"
}

func fetchOpenAIModels(httpClient *http.Client, ctx context.Context, target model.ChannelConfig, key, url string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	for _, header := range target.CustomHeader {
		if header.HeaderKey != "" {
			req.Header.Set(header.HeaderKey, header.HeaderValue)
		}
	}
	response, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	result, err := decodeModelList[model.OpenAIModelList](response)
	if err != nil {
		return nil, err
	}
	models := make([]string, 0, len(result.Data))
	for _, m := range result.Data {
		models = append(models, m.ID)
	}
	return models, nil
}

func fetchAnthropicModels(httpClient *http.Client, ctx context.Context, target model.ChannelConfig, key, url string) ([]string, error) {
	var allModels []string
	var afterID string
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("X-Api-Key", key)
		req.Header.Set("Anthropic-Version", "2023-06-01")
		for _, header := range target.CustomHeader {
			if header.HeaderKey != "" {
				req.Header.Set(header.HeaderKey, header.HeaderValue)
			}
		}
		if afterID != "" {
			q := req.URL.Query()
			q.Set("after_id", afterID)
			req.URL.RawQuery = q.Encode()
		}
		response, err := httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		result, err := decodeModelList[model.AnthropicModelList](response)
		if err != nil {
			return nil, err
		}
		for _, m := range result.Data {
			allModels = append(allModels, m.ID)
		}
		if !result.HasMore {
			break
		}
		afterID = result.LastID
	}
	return allModels, nil
}

func decodeModelList[T any](response *http.Response) (T, error) {
	defer response.Body.Close()
	var result T
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		body, err := io.ReadAll(io.LimitReader(response.Body, 512))
		if err != nil {
			return result, fmt.Errorf("upstream %s", response.Status)
		}
		return result, fmt.Errorf("upstream %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return result, err
	}
	return result, nil
}
```

`internal/helper/models.go` 的 import 需要包含 `encoding/json`、`io`、`net/http`、`path`、`strings`、`sync`、`context`、`fmt`。

- [ ] **Step 2b: 用 httptest 验证两侧协议位**

在 `internal/helper/models_test.go` 增加：

```go
func TestFetchModelsProtocols(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer test-key":
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-test"}]}`))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"claude-test"}],"has_more":false}`))
		}
	}))
	defer server.Close()
	config := model.ChannelConfig{
		BaseURL:                  server.URL,
		OpenAIChatCompletionPath: "/v1/chat/completions",
		AnthropicMessagePath:     "/v1/messages",
	}
	models, err := FetchModels(context.Background(), config, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]model.Protocol, len(models))
	for _, m := range models {
		got[m.Name] = m.Protocols
	}
	if got["gpt-test"] != model.ProtocolOpenAIChatCompletion {
		t.Fatalf("gpt-test = %d", got["gpt-test"])
	}
	if got["claude-test"] != model.ProtocolAnthropicMessage {
		t.Fatalf("claude-test = %d", got["claude-test"])
	}
}
```

（测试文件需要 import `context`、`net/http`、`net/http/httptest`、`testing`、`model`。）

- [ ] **Step 3: 改造 handler**

`internal/server/handlers/channel.go` 的 `fetchModel` 删除本地 HTTP 客户端、过滤和合并逻辑，改为：

```go
models, err := helper.FetchModels(ctx, target, request.Key)
if err != nil {
	resp.Error(c, http.StatusBadGateway, err.Error())
	return
}
resp.Success(c, models)
```

删除该文件中的 `fetchOpenAIModels`、`fetchAnthropicModels`、`modelsURL` 以及不再使用的 import。

- [ ] **Step 4: 验证**

Run: `go test ./internal/helper ./internal/server/handlers`

Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/helper internal/server/handlers/channel.go
git commit -m "refactor: :recycle: 抽取渠道模型探测逻辑"
```

---

### Task 4: 增量同步事务

**Files:**
- Create: `internal/op/channel_sync.go`
- Create: `internal/op/channel_sync_test.go`
- Modify: `internal/op/channel.go`（复用删除授权时的清空逻辑）

**Interfaces:**
- Produces:
  - `op.ChannelModelsSync(channelID int, fetched map[string][]model.ChannelFetchModel, syncedKeys []string, ctx context.Context) (op.ChannelModelSyncResult, error)`
  - `op.ChannelAutoSyncList() []model.Channel`
  - `op.ChannelModelSyncResult{Added []string; Deleted []string}`

- [ ] **Step 1: 写失败测试**

创建 `internal/op/channel_sync_test.go`：

```go
package op

import (
	"testing"

	"github.com/bestruirui/octopus/internal/model"
)

func TestBuildChannelModelSyncPlan(t *testing.T) {
	existing := []model.ChannelModel{
		{ID: 1, Name: "manual-model", Source: model.ChannelModelSourceManual},
		{ID: 2, Name: "old-auto", Source: model.ChannelModelSourceAuto},
		{ID: 3, Name: "kept-auto", Source: model.ChannelModelSourceAuto},
	}
	fetched := map[string][]model.ChannelFetchModel{
		"key-a": {
			{Name: "manual-model", Protocols: model.ProtocolOpenAIChatCompletion},
			{Name: "new-auto", Protocols: model.ProtocolOpenAIChatCompletion},
			{Name: "kept-auto", Protocols: model.ProtocolAnthropicMessage},
		},
	}
	plan := buildChannelModelSyncPlan(existing, fetched, []string{"key-a"})
	if len(plan.create) != 1 || plan.create[0] != "new-auto" {
		t.Fatalf("create = %v", plan.create)
	}
	if len(plan.deleteIDs) != 1 || plan.deleteIDs[0] != 2 {
		t.Fatalf("deleteIDs = %v", plan.deleteIDs)
	}
	if _, ok := plan.desired["manual-model"]; ok {
		t.Fatalf("manual model must not be managed")
	}
	if got := plan.desired["new-auto"]["key-a"]; got != model.ProtocolOpenAIChatCompletion {
		t.Fatalf("new-auto protocols = %d", got)
	}
	if _, ok := plan.desired["kept-auto"]["key-a"]; !ok {
		t.Fatalf("kept-auto grant missing")
	}
}

func TestBuildChannelModelSyncPlanFailedKeyUntouched(t *testing.T) {
	existing := []model.ChannelModel{{ID: 1, Name: "m", Source: model.ChannelModelSourceAuto}}
	fetched := map[string][]model.ChannelFetchModel{"key-a": {{Name: "m", Protocols: model.ProtocolOpenAIChatCompletion}}}
	plan := buildChannelModelSyncPlan(existing, fetched, []string{"key-a"})
	if _, ok := plan.desired["m"]["key-b"]; ok {
		t.Fatalf("failed key must not receive grants")
	}
}
```

Run: `go test ./internal/op`

Expected: FAIL，`buildChannelModelSyncPlan` 未定义。

- [ ] **Step 2: 实现计划与事务**

创建 `internal/op/channel_sync.go`：

```go
package op

import (
	"context"
	"fmt"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"gorm.io/gorm"
)

type ChannelModelSyncResult struct {
	Added   []string // 本轮新建的自动模型名称。
	Deleted []string // 本轮删除的自动模型名称。
}

type channelModelSyncPlan struct {
	create    []string                                  // 需要新建的自动模型。
	deleteIDs []int                                     // 需要删除的自动模型主键。
	desired   map[string]map[string]model.Protocol      // 模型名 -> 凭据名 -> 协议位, 仅自动模型。
}

// buildChannelModelSyncPlan 计算本轮模型增删与授权目标。
// 手动模型永不参与删除或授权覆盖; 只有探测成功的凭据会进入 desired。
func buildChannelModelSyncPlan(
	existing []model.ChannelModel,
	fetched map[string][]model.ChannelFetchModel,
	syncedKeys []string,
) channelModelSyncPlan {
	manual := make(map[string]struct{})
	autoByName := make(map[string]model.ChannelModel)
	for _, channelModel := range existing {
		if channelModel.Source == model.ChannelModelSourceManual {
			manual[channelModel.Name] = struct{}{}
			continue
		}
		autoByName[channelModel.Name] = channelModel
	}
	allFetched := make(map[string]struct{})
	for _, models := range fetched {
		for _, fetchedModel := range models {
			allFetched[fetchedModel.Name] = struct{}{}
		}
	}
	plan := channelModelSyncPlan{
		desired: make(map[string]map[string]model.Protocol),
	}
	for name := range allFetched {
		if _, ok := manual[name]; ok {
			continue
		}
		if _, ok := autoByName[name]; !ok {
			plan.create = append(plan.create, name)
		}
	}
	for name, channelModel := range autoByName {
		if _, ok := allFetched[name]; !ok {
			plan.deleteIDs = append(plan.deleteIDs, channelModel.ID)
		}
	}
	for _, keyName := range syncedKeys {
		for _, fetchedModel := range fetched[keyName] {
			if _, ok := manual[fetchedModel.Name]; ok {
				continue
			}
			if plan.desired[fetchedModel.Name] == nil {
				plan.desired[fetchedModel.Name] = make(map[string]model.Protocol)
			}
			plan.desired[fetchedModel.Name][keyName] = fetchedModel.Protocols
		}
	}
	return plan
}

// ChannelModelsSync 按探测结果增量更新自动模型与授权。
// 手动模型和未探测成功的凭据保持不变; 删除的自动模型连同授权一起移除。
func ChannelModelsSync(
	channelID int,
	fetched map[string][]model.ChannelFetchModel,
	syncedKeys []string,
	ctx context.Context,
) (ChannelModelSyncResult, error) {
	result := ChannelModelSyncResult{}
	err := db.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing []model.ChannelModel
		if err := tx.Where("channel_id = ?", channelID).Find(&existing).Error; err != nil {
			return fmt.Errorf("failed to load channel models: %w", err)
		}
		plan := buildChannelModelSyncPlan(existing, fetched, syncedKeys)
		result.Added = append(result.Added, plan.create...)
		for _, channelModel := range existing {
			for _, id := range plan.deleteIDs {
				if channelModel.ID == id {
					result.Deleted = append(result.Deleted, channelModel.Name)
				}
			}
		}
		if len(plan.deleteIDs) > 0 {
			if err := clearActiveItems(tx, grantsOf(tx, "channel_model_id", plan.deleteIDs)); err != nil {
				return err
			}
			if err := tx.Where("channel_model_id IN ?", plan.deleteIDs).
				Delete(&model.ChannelGrant{}).Error; err != nil {
				return fmt.Errorf("failed to delete channel grants: %w", err)
			}
			if err := tx.Where("id IN ?", plan.deleteIDs).
				Delete(&model.ChannelModel{}).Error; err != nil {
				return fmt.Errorf("failed to delete channel models: %w", err)
			}
		}
		modelIDByName := make(map[string]int, len(existing)+len(plan.create))
		for _, channelModel := range existing {
			modelIDByName[channelModel.Name] = channelModel.ID
		}
		for _, name := range plan.create {
			channelModel := model.ChannelModel{
				ChannelID: channelID,
				Name:      name,
				Source:    model.ChannelModelSourceAuto,
			}
			if err := tx.Create(&channelModel).Error; err != nil {
				return fmt.Errorf("failed to create channel model: %w", err)
			}
			modelIDByName[name] = channelModel.ID
		}
		var channelKeys []model.ChannelKey
		if err := tx.Where("channel_id = ?", channelID).Find(&channelKeys).Error; err != nil {
			return fmt.Errorf("failed to load channel keys: %w", err)
		}
		keyIDByName := make(map[string]int, len(channelKeys))
		keyNameByID := make(map[int]string, len(channelKeys))
		for _, channelKey := range channelKeys {
			keyIDByName[channelKey.Name] = channelKey.ID
			keyNameByID[channelKey.ID] = channelKey.Name
		}
		var allModels []model.ChannelModel
		if err := tx.Where("channel_id = ?", channelID).Find(&allModels).Error; err != nil {
			return fmt.Errorf("failed to reload channel models: %w", err)
		}
		autoModelIDs := make([]int, 0)
		autoModelNameByID := make(map[int]string)
		for _, channelModel := range allModels {
			if channelModel.Source == model.ChannelModelSourceAuto {
				autoModelIDs = append(autoModelIDs, channelModel.ID)
				autoModelNameByID[channelModel.ID] = channelModel.Name
			}
		}
		var existingGrants []model.ChannelGrant
		if len(autoModelIDs) > 0 {
			if err := tx.Where("channel_model_id IN ?", autoModelIDs).Find(&existingGrants).Error; err != nil {
				return fmt.Errorf("failed to load channel grants: %w", err)
			}
		}
		type grantKey struct {
			modelID int
			keyID   int
		}
		existingByKey := make(map[grantKey]model.ChannelGrant, len(existingGrants))
		for _, grant := range existingGrants {
			existingByKey[grantKey{grant.ChannelModelID, grant.ChannelKeyID}] = grant
		}
		syncedKeyID := make(map[int]struct{}, len(syncedKeys))
		for _, keyName := range syncedKeys {
			if id, ok := keyIDByName[keyName]; ok {
				syncedKeyID[id] = struct{}{}
			}
		}
		for _, modelID := range autoModelIDs {
			modelName := autoModelNameByID[modelID]
			for keyID := range syncedKeyID {
				keyName := keyNameByID[keyID]
				desired := plan.desired[modelName][keyName]
				key := grantKey{modelID, keyID}
				current, exists := existingByKey[key]
				switch {
				case desired == 0 && exists:
					if err := tx.Delete(&model.ChannelGrant{}, current.ID).Error; err != nil {
						return fmt.Errorf("failed to delete channel grant: %w", err)
					}
					delete(existingByKey, key)
				case desired != 0 && exists && current.Protocols != desired:
					if err := tx.Model(&model.ChannelGrant{}).Where("id = ?", current.ID).
						Update("protocols", desired).Error; err != nil {
						return fmt.Errorf("failed to update channel grant: %w", err)
					}
					delete(existingByKey, key)
				case desired != 0 && !exists:
					if err := tx.Create(&model.ChannelGrant{
						ChannelModelID: modelID,
						ChannelKeyID:   keyID,
						Protocols:      desired,
					}).Error; err != nil {
						return fmt.Errorf("failed to create channel grant: %w", err)
					}
					delete(existingByKey, key)
				}
			}
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	if err := reloadChannelChildren(ctx, channelID); err != nil {
		return result, err
	}
	if err := groupRefreshCache(ctx); err != nil {
		return result, fmt.Errorf("failed to refresh groups: %w", err)
	}
	return result, nil
}

// ChannelAutoSyncList 返回开启自动同步的渠道配置。
func ChannelAutoSyncList() []model.Channel {
	channels := make([]model.Channel, 0)
	for _, channel := range channelCache.GetAll() {
		if channel.AutoSync {
			channels = append(channels, channel)
		}
	}
	return channels
}
```

如果 `keyIDByName` 反查过于绕，改为先建 `keyNameByID` 映射：

```go
keyNameByID := make(map[int]string, len(channelKeys))
for _, channelKey := range channelKeys {
	keyIDByName[channelKey.Name] = channelKey.ID
	keyNameByID[channelKey.ID] = channelKey.Name
}
```

然后用 `keyName := keyNameByID[keyID]; desired := plan.desired[modelName][keyName]`。

- [ ] **Step 3: 处理残留授权**

上面的循环只遍历本渠道自动模型的授权。`existingByKey` 中剩余的条目理论上都属于未同步成功的凭据或手动模型，不应删除。若来自已同步成功的凭据但模型已从 `plan.desired` 移除，已在上面的 `desired == 0` 分支处理。确认没有未使用的变量。

- [ ] **Step 4: 验证**

Run: `go test ./internal/op`

Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/op/channel_sync.go internal/op/channel_sync_test.go
git commit -m "feat: :sparkles: 增加渠道模型增量同步事务"
```

---

### Task 5: 定时任务、设置项与同步 API

**Files:**
- Modify: `internal/model/setting.go`
- Modify: `internal/server/handlers/setting.go:60-74`
- Modify: `internal/task/init.go`
- Create: `internal/task/sync.go`
- Modify: `internal/server/handlers/channel.go`（路由与 handler）

**Interfaces:**
- Consumes: `op.ChannelAutoSyncList`、`op.ChannelModelsSync`、`helper.FetchModels`
- Produces:
  - `task.SyncModelsTask() error`
  - `task.GetLastSyncModelsTime() time.Time`
  - `POST /api/v1/channel/sync`
  - `GET /api/v1/channel/last-sync-time`

- [ ] **Step 1: 恢复设置项**

`internal/model/setting.go`：

```go
SettingKeySyncLLMInterval SettingKey = "sync_llm_interval" // 渠道模型同步间隔(小时)
```

默认值：

```go
{Key: SettingKeySyncLLMInterval, Value: "24"},
```

`Validate()` 的整数分支：

```go
case SettingKeyModelInfoUpdateInterval, SettingKeySyncLLMInterval:
```

- [ ] **Step 2: 设置变更时更新任务**

`internal/server/handlers/setting.go` 的 switch：

```go
case model.SettingKeyModelInfoUpdateInterval, model.SettingKeySyncLLMInterval:
	hours, err := strconv.Atoi(setting.Value)
	if err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	task.Update(string(setting.Key), time.Duration(hours)*time.Hour)
```

- [ ] **Step 3: 注册定时任务**

`internal/task/init.go` 增加常量：

```go
TaskSyncModels = "sync_models"
```

在价格任务注册之后增加：

```go
syncIntervalHours, err := op.SettingGetInt(model.SettingKeySyncLLMInterval)
if err != nil {
	log.Warnf("failed to get sync llm interval: %v", err)
} else {
	Register(string(model.SettingKeySyncLLMInterval), time.Duration(syncIntervalHours)*time.Hour, true, func() {
		if err := SyncModelsTask(); err != nil {
			log.Warnf("failed to sync models: %v", err)
		}
	})
}
```

- [ ] **Step 4: 实现同步任务**

创建 `internal/task/sync.go`：

```go
package task

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/helper"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/price"
	"github.com/charmbracelet/log"
)

var (
	syncModelsMu         sync.Mutex
	lastSyncModelsTimeMu sync.RWMutex
	lastSyncModelsTime   = time.Now()
)

// SyncModelsTask 同步所有开启自动同步的渠道模型。
func SyncModelsTask() error {
	if !syncModelsMu.TryLock() {
		return fmt.Errorf("model sync already running")
	}
	defer syncModelsMu.Unlock()

	startTime := time.Now()
	defer func() {
		lastSyncModelsTimeMu.Lock()
		lastSyncModelsTime = time.Now()
		lastSyncModelsTimeMu.Unlock()
		log.Debugf("sync models task finished, sync time: %s", time.Since(startTime))
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	channels := op.ChannelAutoSyncList()
	var firstErr error
	for _, channel := range channels {
		detail, err := op.ChannelDetailGet(channel.ID)
		if err != nil {
			log.Warnf("failed to load channel %s: %v", channel.Name, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		fetched := make(map[string][]model.ChannelFetchModel)
		syncedKeys := make([]string, 0, len(detail.Keys))
		for _, channelKey := range detail.Keys {
			if !channelKey.Enabled {
				continue
			}
			models, err := helper.FetchModels(ctx, detail.ChannelConfig, channelKey.Key)
			if err != nil {
				log.Warnf("failed to fetch models for channel %s key %s: %v", channel.Name, channelKey.Name, err)
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			fetched[channelKey.Name] = models
			syncedKeys = append(syncedKeys, channelKey.Name)
		}
		if len(syncedKeys) == 0 {
			continue
		}
		result, err := op.ChannelModelsSync(channel.ID, fetched, syncedKeys, ctx)
		if err != nil {
			log.Warnf("failed to sync models for channel %s: %v", channel.Name, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if len(result.Added) > 0 {
			if err := addSyncedModelPrices(result.Added, ctx); err != nil {
				log.Warnf("failed to add model prices for channel %s: %v", channel.Name, err)
			}
			log.Infof("added channel %s models: %v", channel.Name, result.Added)
		}
		if len(result.Deleted) > 0 {
			log.Infof("deleted channel %s models: %v", channel.Name, result.Deleted)
		}
	}
	if err := op.LLMCleanupGhosts(ctx); err != nil {
		log.Warnf("failed to clean ghost model prices: %v", err)
	}
	return firstErr
}

func addSyncedModelPrices(names []string, ctx context.Context) error {
	seen := make(map[string]struct{}, len(names))
	llmInfos := make([]model.LLMInfo, 0, len(names))
	for _, name := range names {
		name = strings.ToLower(name)
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		llmInfo := model.LLMInfo{Name: name}
		if modelPrice := price.GetLLMPrice(name); modelPrice != nil {
			llmInfo.LLMPrice = *modelPrice
		}
		llmInfos = append(llmInfos, llmInfo)
	}
	return op.LLMBatchCreate(llmInfos, ctx)
}

// GetLastSyncModelsTime 返回最近一次模型同步结束时间。
func GetLastSyncModelsTime() time.Time {
	lastSyncModelsTimeMu.RLock()
	defer lastSyncModelsTimeMu.RUnlock()
	return lastSyncModelsTime
}
```

- [ ] **Step 5: 恢复同步 API**

在 `internal/server/handlers/channel.go` 的 `init()` 中增加第二个路由组（不要带 `RequireJSON`）：

```go
router.NewGroupRouter("/api/v1/channel").
	Use(middleware.Auth()).
	AddRoute(
		router.NewRoute("/sync", http.MethodPost).
			Handle(syncChannel),
	).
	AddRoute(
		router.NewRoute("/last-sync-time", http.MethodGet).
			Handle(getLastSyncTime),
	)
```

新增 handler：

```go
func syncChannel(c *gin.Context) {
	if err := task.SyncModelsTask(); err != nil {
		if err.Error() == "model sync already running" {
			resp.Error(c, http.StatusConflict, err.Error())
			return
		}
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, nil)
}

func getLastSyncTime(c *gin.Context) {
	resp.Success(c, task.GetLastSyncModelsTime())
}
```

增加 import `"github.com/bestruirui/octopus/internal/task"`。

- [ ] **Step 6: 验证**

Run: `go build ./... && go test ./internal/task ./internal/op ./internal/helper`

Expected: PASS。

- [ ] **Step 7: 提交**

```bash
git add internal/model/setting.go internal/server/handlers/setting.go internal/task internal/server/handlers/channel.go
git commit -m "feat: :sparkles: 恢复渠道模型定时同步"
```

---

### Task 6: 前端自动同步界面

**Files:**
- Modify: `web/src/api/channel.ts`
- Modify: `web/src/api/setting.ts`
- Modify: `web/src/components/modules/channel/state.ts`
- Modify: `web/src/components/modules/channel/Form.tsx`
- Create: `web/src/components/modules/setting/LLMSync.tsx`
- Modify: `web/src/components/modules/setting/index.tsx`
- Modify: `web/src/locales/zh_hans.json`、`zh_hant.json`、`en.json`

**Interfaces:**
- Consumes: `auto_sync` 字段、`/channel/sync`、`/channel/last-sync-time`
- Produces: 渠道高级步骤开关、设置页同步卡片。

- [ ] **Step 1: 更新 API 类型与 hooks**

`ChannelDetail` 增加：

```ts
auto_sync: boolean; // 是否参与定时同步上游模型。
```

新增 hooks：

```ts
export function useSyncChannel() {
    return useMutation({
        mutationFn: () => apiRequest<null>('/api/v1/channel/sync', { method: 'POST' }),
    });
}

export function useLastSyncTime() {
    return useQuery({
        queryKey: ['channels', 'last-sync-time'],
        queryFn: () => apiRequest<string>('/api/v1/channel/last-sync-time'),
        refetchInterval: 30000,
    });
}
```

`web/src/api/setting.ts` 的 `SettingKey` 增加：

```ts
SyncLLMInterval: 'sync_llm_interval',
```

- [ ] **Step 2: 表单状态接入 auto_sync**

`state.ts` 的类型、初始值、`fromChannel`、`toChannelConfig` 都增加 `auto_sync`：

```ts
auto_sync: false,
```

```ts
auto_sync: channel.auto_sync,
```

```ts
auto_sync: state.auto_sync,
```

- [ ] **Step 3: 高级步骤增加开关**

`Form.tsx` 的 `step === 'advanced'` 区域最上方增加：

```tsx
<label className="flex items-center gap-2 cursor-pointer">
    <Switch
        checked={state.auto_sync}
        onCheckedChange={(checked) => setState({ ...state, auto_sync: checked })}
    />
    <span className="text-sm">{t('autoSync')}</span>
</label>
```

- [ ] **Step 4: 设置页同步卡片**

创建 `LLMSync.tsx`，基于旧版实现但使用当前 UI 组件：

```tsx
import { useEffect, useRef, useState } from 'react';
import { useTranslations } from 'use-intl';
import { Clock, RefreshCw } from 'lucide-react';
import { toast } from 'sonner';
import { Input } from '@/components/ui/input';
import { Button } from '@/components/ui/button';
import { SettingKey, useSetSetting, useSettingList } from '@/api/setting';
import { useLastSyncTime, useSyncChannel } from '@/api/channel';

export function SettingLLMSync() {
    const t = useTranslations('setting');
    const { data: settings } = useSettingList();
    const setSetting = useSetSetting();
    const syncChannel = useSyncChannel();
    const { data: lastSyncTime } = useLastSyncTime();
    const [syncInterval, setSyncInterval] = useState('');
    const initialSyncInterval = useRef('');

    useEffect(() => {
        const interval = settings?.find((s) => s.key === SettingKey.SyncLLMInterval);
        if (interval) {
            queueMicrotask(() => setSyncInterval(interval.value));
            initialSyncInterval.current = interval.value;
        }
    }, [settings]);

    const handleSave = (value: string) => {
        if (value === initialSyncInterval.current) return;
        setSetting.mutate({ key: SettingKey.SyncLLMInterval, value }, {
            onSuccess: () => {
                toast.success(t('saved'));
                initialSyncInterval.current = value;
            },
        });
    };

    const formatLastSyncTime = (value: string | undefined) => {
        if (!value) return t('llmSync.neverSynced');
        const date = new Date(value);
        if (Number.isNaN(date.getTime()) || date.getFullYear() === 1) return t('llmSync.neverSynced');
        return date.toLocaleString();
    };

    return (
        <div className="rounded-3xl border border-border bg-card p-6 space-y-5">
            <h2 className="text-lg font-bold text-card-foreground flex items-center gap-2">
                <RefreshCw className="h-5 w-5" />
                {t('llmSync.title')}
            </h2>
            <div className="flex items-center justify-between gap-4">
                <div className="flex items-center gap-3">
                    <Clock className="h-5 w-5 text-muted-foreground" />
                    <span className="text-sm font-medium">{t('llmSync.syncInterval.label')}</span>
                </div>
                <Input
                    type="number"
                    value={syncInterval}
                    onChange={(e) => setSyncInterval(e.target.value)}
                    onBlur={() => handleSave(syncInterval)}
                    placeholder={t('llmSync.syncInterval.placeholder')}
                    className="w-48 rounded-xl"
                />
            </div>
            <div className="flex items-center justify-between gap-4">
                <div className="flex flex-col gap-1">
                    <div className="flex items-center gap-3">
                        <RefreshCw className="h-5 w-5 text-muted-foreground" />
                        <span className="text-sm font-medium">{t('llmSync.manualSync.label')}</span>
                    </div>
                    <span className="text-xs text-muted-foreground ml-8">
                        {t('llmSync.lastSync')}: {formatLastSyncTime(lastSyncTime)}
                    </span>
                </div>
                <Button
                    variant="outline"
                    size="sm"
                    onClick={() => syncChannel.mutate(undefined, {
                        onSuccess: () => toast.success(t('llmSync.syncSuccess')),
                        onError: (error) => toast.error(t('llmSync.syncFailed'), {
                            description: error instanceof Error ? error.message : undefined,
                        }),
                    })}
                    disabled={syncChannel.isPending}
                    className="rounded-xl"
                >
                    {syncChannel.isPending ? t('llmSync.manualSync.syncing') : t('llmSync.manualSync.button')}
                </Button>
            </div>
        </div>
    );
}
```

`setting/index.tsx` 引入并渲染 `<SettingLLMSync />`，放在 `<SettingLLMPrice />` 之后。

- [ ] **Step 5: 翻译**

三份 locale 的 `channel.form` 增加 `autoSync`；`setting` 增加 `llmSync`。文案沿用旧版：

- zh_hans：`自动同步`、`渠道同步`、`自动同步间隔（小时）`、`手动同步渠道`、`立即同步`、`同步中...`、`上次同步`、`从未同步`、`渠道同步成功`、`渠道同步失败`
- zh_hant：`自動同步`、`供應源同步`、`自動同步間隔（小時）`、`手動同步供應源`、`立即同步`、`同步中...`、`上次同步`、`從未同步`、`供應源同步成功`、`供應源同步失敗`
- en：`Auto Sync`、`Channel Sync`、`Auto Sync Interval (hours)`、`Manual Channel Sync`、`Sync Now`、`Syncing...`、`Last Sync`、`Never synced`、`Channel synced successfully`、`Failed to sync channel`

- [ ] **Step 6: 验证**

Run: `cd web && npm exec --yes pnpm@11.10.0 -- run build`

Expected: `tsc --noEmit` 与 `vite build` 均通过。

- [ ] **Step 7: 提交**

```bash
git add web/src/api web/src/components/modules/channel web/src/components/modules/setting web/src/locales
git commit -m "feat: :sparkles: 恢复渠道同步前端入口"
```

---

### Task 7: 渠道/模型按天统计

**Files:**
- Modify: `internal/model/stats.go`
- Modify: `internal/db/db.go:62-76`
- Modify: `internal/op/stats.go`
- Modify: `internal/relay/handler.go:215-219`, `263-265`, `348-350`
- Modify: `internal/op/channel.go`（删除渠道/模型时清理按天数据）
- Create: `internal/op/stats_period_test.go`

**Interfaces:**
- Produces:
  - `model.ChannelStatsDaily`、`model.ChannelModelStatsDaily`
  - `op.ChannelStatsDailyUpdate(channelID int, metrics model.StatsMetrics)`
  - `op.ChannelModelStatsDailyUpdate(channelModelID int, metrics model.StatsMetrics)`
  - `op.ChannelStatsListPeriod(period int) []model.ChannelStats`

- [ ] **Step 1: 增加统计模型**

`internal/model/stats.go` 追加：

```go
type ChannelStatsDaily struct {
	ChannelID int    `json:"channel_id" gorm:"primaryKey;index:idx_channel_stats_daily_date"`
	Date      string `json:"date" gorm:"primaryKey;index:idx_channel_stats_daily_date"`
	StatsMetrics
}

type ChannelModelStatsDaily struct {
	ChannelModelID int    `json:"channel_model_id" gorm:"primaryKey;index:idx_channel_model_stats_daily_date"`
	Date           string `json:"date" gorm:"primaryKey;index:idx_channel_model_stats_daily_date"`
	StatsMetrics
}
```

`internal/db/db.go` 的 AutoMigrate 列表加入：

```go
&model.ChannelStatsDaily{},
&model.ChannelModelStatsDaily{},
```

- [ ] **Step 2: 写失败测试**

创建 `internal/op/stats_period_test.go`：

```go
package op

import (
	"testing"
	"time"
)

func TestStatsPeriodSince(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.Local)
	cases := []struct {
		period int
		want   string
	}{
		{1, "20261026"},
		{7, "20261020"},
		{30, "20260927"},
	}
	for _, tc := range cases {
		if got := statsPeriodSince(tc.period, now); got != tc.want {
			t.Fatalf("period %d: got %s, want %s", tc.period, got, tc.want)
		}
	}
}
```

Run: `go test ./internal/op -run TestStatsPeriodSince`

Expected: FAIL，`statsPeriodSince` 未定义。

- [ ] **Step 3: 增加内存缓存与写入函数**

在 `internal/op/stats.go` 增加：

```go
var channelStatsDailyCache = make(map[int]model.ChannelStatsDaily)
var channelStatsDailyCacheLock sync.Mutex
var channelStatsDailyNeedUpdate = make(map[int]struct{})

var channelModelStatsDailyCache = make(map[int]model.ChannelModelStatsDaily)
var channelModelStatsDailyCacheLock sync.Mutex
var channelModelStatsDailyNeedUpdate = make(map[int]struct{})

func statsPeriodSince(period int, now time.Time) string {
	days := period - 1
	if days < 0 {
		days = 0
	}
	return now.AddDate(0, 0, -days).Format("20060102")
}

func ChannelStatsDailyUpdate(channelID int, metrics model.StatsMetrics) {
	today := time.Now().Format("20060102")
	channelStatsDailyCacheLock.Lock()
	entry := channelStatsDailyCache[channelID]
	if entry.Date != today {
		entry = model.ChannelStatsDaily{ChannelID: channelID, Date: today}
	}
	entry.StatsMetrics.Add(metrics)
	channelStatsDailyCache[channelID] = entry
	channelStatsDailyNeedUpdate[channelID] = struct{}{}
	channelStatsDailyCacheLock.Unlock()
}

func ChannelModelStatsDailyUpdate(channelModelID int, metrics model.StatsMetrics) {
	today := time.Now().Format("20060102")
	channelModelStatsDailyCacheLock.Lock()
	entry := channelModelStatsDailyCache[channelModelID]
	if entry.Date != today {
		entry = model.ChannelModelStatsDaily{ChannelModelID: channelModelID, Date: today}
	}
	entry.StatsMetrics.Add(metrics)
	channelModelStatsDailyCache[channelModelID] = entry
	channelModelStatsDailyNeedUpdate[channelModelID] = struct{}{}
	channelModelStatsDailyCacheLock.Unlock()
}
```

- [ ] **Step 4: 持久化与启动加载**

在 `StatsSaveDB` 中增加 daily 缓存的写入：

```go
func persistDailyChannelStats(dbConn *gorm.DB) error {
	channelStatsDailyCacheLock.Lock()
	rows := make([]model.ChannelStatsDaily, 0, len(channelStatsDailyNeedUpdate))
	for id := range channelStatsDailyNeedUpdate {
		if entry, ok := channelStatsDailyCache[id]; ok {
			rows = append(rows, entry)
		}
	}
	channelStatsDailyNeedUpdate = make(map[int]struct{})
	channelStatsDailyCacheLock.Unlock()
	if len(rows) > 0 {
		if err := dbConn.Clauses(clause.OnConflict{UpdateAll: true}).Create(&rows).Error; err != nil {
			return err
		}
	}
	// 同样处理 channelModelStatsDailyNeedUpdate / channelModelStatsDailyCache。
	return nil
}
```

在 `StatsSaveDB` 与 `statsSaveDBWithDailyOverride` 中调用。注意失败时把 ID 放回待写集合，仿照现有 `restoreDirtySet`。

在 `statsRefreshCache` 中加载今天的数据：

```go
today := time.Now().Format("20060102")
var channelDaily []model.ChannelStatsDaily
if err := dbConn.Where("date = ?", today).Find(&channelDaily).Error; err != nil {
	return fmt.Errorf("failed to load channel daily stats: %v", err)
}
channelStatsDailyCacheLock.Lock()
channelStatsDailyCache = make(map[int]model.ChannelStatsDaily, len(channelDaily))
for _, row := range channelDaily {
	channelStatsDailyCache[row.ChannelID] = row
}
channelStatsDailyCacheLock.Unlock()
// 同样加载 ChannelModelStatsDaily。
```

清理 90 天前的数据：

```go
cutoff := time.Now().AddDate(0, 0, -90).Format("20060102")
_ = dbConn.Where("date < ?", cutoff).Delete(&model.ChannelStatsDaily{}).Error
_ = dbConn.Where("date < ?", cutoff).Delete(&model.ChannelModelStatsDaily{}).Error
```

- [ ] **Step 5: relay 写入**

在 `internal/relay/handler.go` 三处调用 `ChannelStatsUpdate` / `ChannelModelStatsUpdate` 的位置后各加两行：

```go
op.ChannelStatsDailyUpdate(channel.ID, metrics)
op.ChannelModelStatsDailyUpdate(channelModel.ID, metrics)
```

- [ ] **Step 6: 删除时清理**

- `syncChannelModels` 删除模型处，在删除 `ChannelModel` 前：

```go
if err := tx.Where("channel_model_id IN ?", deletedModelIDs).
	Delete(&model.ChannelModelStatsDaily{}).Error; err != nil {
	return fmt.Errorf("failed to delete channel model daily stats: %w", err)
}
```

- `ChannelDel` 删除渠道前，先查出该渠道的模型 ID：

```go
var modelIDs []int
if err := tx.Model(&model.ChannelModel{}).Where("channel_id = ?", id).Pluck("id", &modelIDs).Error; err != nil {
	return err
}
if len(modelIDs) > 0 {
	if err := tx.Where("channel_model_id IN ?", modelIDs).
		Delete(&model.ChannelModelStatsDaily{}).Error; err != nil {
		return err
	}
}
if err := tx.Where("channel_id = ?", id).Delete(&model.ChannelStatsDaily{}).Error; err != nil {
	return err
}
```

- [ ] **Step 7: 查询聚合**

在 `internal/op/stats.go` 增加：

```go
// ChannelStatsListPeriod 汇总周期内的渠道与模型统计; period 为 1/7/30。
// 今天取内存数据, 其余日期取数据库, 避免同一天被重复计算。
func ChannelStatsListPeriod(period int) []model.ChannelStats {
	today := time.Now().Format("20060102")
	since := statsPeriodSince(period, time.Now())

	channelTotals := make(map[int]model.StatsMetrics)
	modelTotals := make(map[int]model.StatsMetrics)

	var channelDaily []model.ChannelStatsDaily
	if err := db.GetDB().Where("date >= ? AND date < ?", since, today).Find(&channelDaily).Error; err == nil {
		for _, row := range channelDaily {
			total := channelTotals[row.ChannelID]
			total.Add(row.StatsMetrics)
			channelTotals[row.ChannelID] = total
		}
	}
	var modelDaily []model.ChannelModelStatsDaily
	if err := db.GetDB().Where("date >= ? AND date < ?", since, today).Find(&modelDaily).Error; err == nil {
		for _, row := range modelDaily {
			total := modelTotals[row.ChannelModelID]
			total.Add(row.StatsMetrics)
			modelTotals[row.ChannelModelID] = total
		}
	}
	channelStatsDailyCacheLock.Lock()
	for id, row := range channelStatsDailyCache {
		if row.Date != today {
			continue
		}
		total := channelTotals[id]
		total.Add(row.StatsMetrics)
		channelTotals[id] = total
	}
	channelStatsDailyCacheLock.Unlock()
	channelModelStatsDailyCacheLock.Lock()
	for id, row := range channelModelStatsDailyCache {
		if row.Date != today {
			continue
		}
		total := modelTotals[id]
		total.Add(row.StatsMetrics)
		modelTotals[id] = total
	}
	channelModelStatsDailyCacheLock.Unlock()

	stats := make([]model.ChannelStats, 0)
	for _, channel := range channelCache.GetAll() {
		metrics := channelTotals[channel.ID]
		if metrics.RequestSuccess+metrics.RequestFailed == 0 {
			continue
		}
		models := make([]model.ChannelModelStats, 0)
		for _, channelModel := range channelModelCache.GetAll() {
			if channelModel.ChannelID != channel.ID {
				continue
			}
			modelMetrics := modelTotals[channelModel.ID]
			if modelMetrics.RequestSuccess+modelMetrics.RequestFailed == 0 {
				continue
			}
			models = append(models, model.ChannelModelStats{
				ModelID:      channelModel.ID,
				ModelName:    channelModel.Name,
				StatsMetrics: modelMetrics,
			})
		}
		stats = append(stats, model.ChannelStats{
			ChannelID:    channel.ID,
			ChannelName:  channel.Name,
			Enabled:      channel.Enabled,
			Models:       models,
			StatsMetrics: metrics,
		})
	}
	return stats
}
```

- [ ] **Step 8: 验证**

Run: `go test ./internal/op -run TestStatsPeriodSince && go build ./...`

Expected: PASS。

- [ ] **Step 9: 提交**

```bash
git add internal/model/stats.go internal/db/db.go internal/op/stats.go internal/op/stats_period_test.go internal/relay/handler.go internal/op/channel.go
git commit -m "feat: :sparkles: 增加渠道与模型按天统计"
```

---

### Task 8: 周期查询接口与首页联动

**Files:**
- Modify: `internal/server/handlers/channel.go`（`listChannelStats`）
- Modify: `web/src/api/channel.ts`（`useChannelStats`）
- Modify: `web/src/components/modules/home/rank.tsx`

**Interfaces:**
- Consumes: `op.ChannelStatsListPeriod`
- Produces: `GET /api/v1/channel/stats?period=1|7|30`

- [ ] **Step 1: 后端解析 period**

`listChannelStats` 改为：

```go
func listChannelStats(c *gin.Context) {
	periodText := c.Query("period")
	if periodText == "" {
		resp.Success(c, op.ChannelStatsList())
		return
	}
	period, err := strconv.Atoi(periodText)
	if err != nil || (period != 1 && period != 7 && period != 30) {
		resp.Error(c, http.StatusBadRequest, "period must be 1, 7 or 30")
		return
	}
	resp.Success(c, op.ChannelStatsListPeriod(period))
}
```

- [ ] **Step 2: 前端查询带周期**

把 `channelStatsFormattedQueryOptions` 替换为函数：

```ts
function channelStatsQueryOptions(period?: 1 | 7 | 30) {
    return queryOptions({
        queryKey: ['channels', 'stats', period ?? 'all'],
        queryFn: () => apiRequest<ChannelStats[]>(
            `/api/v1/channel/stats${period ? `?period=${period}` : ''}`,
        ),
        select: (data) => data.map((item): ChannelStatsFormatted => ({
            channel_id: item.channel_id,
            channel_name: item.channel_name,
            enabled: item.enabled,
            models: item.models.map((channelModel) => ({
                model_id: channelModel.model_id,
                model_name: channelModel.model_name,
                formatted: formatStatsMetrics(channelModel),
            })),
            formatted: formatStatsMetrics(item),
        })),
        refetchInterval: 30000,
        refetchOnMount: 'always' as const,
    });
}

export function useChannelStats(period?: 1 | 7 | 30, enabled = true) {
    return useQuery({ ...channelStatsQueryOptions(period), enabled });
}
```

保留旧调用 `useChannelStats()` 的行为（`period` 为 `undefined`）。

- [ ] **Step 3: 首页榜单传入周期**

`rank.tsx`：

```ts
const period = useHomeViewStore((state) => state.chartPeriod);
const periodValue = Number(period) as 1 | 7 | 30;
const { data: channelStats } = useChannelStats(periodValue);
```

删除原来的 `useChannelStats()` 调用。

- [ ] **Step 4: 验证**

Run: `cd web && npm exec --yes pnpm@11.10.0 -- run build`

Expected: PASS。

Run: `go build ./...`

Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/server/handlers/channel.go web/src/api/channel.ts web/src/components/modules/home/rank.tsx
git commit -m "feat: :sparkles: 首页榜单跟随趋势周期"
```

---

### Task 9: 日志后端保留每轮记录

**Files:**
- Modify: `internal/relay/state.go`
- Create: `internal/relay/state_test.go`

**Interfaces:**
- Produces: `RequestState.Rounds []RoundRecord`，JSON 字段 `rounds`。

- [ ] **Step 1: 写失败测试**

创建 `internal/relay/state_test.go`：

```go
package relay

import (
	"fmt"
	"testing"

	"github.com/bestruirui/octopus/internal/model"
)

func TestRequestRoundsKeepHistory(t *testing.T) {
	request := &RequestState{}
	for round := 1; round <= 25; round++ {
		request.startRound(func() {}, "ch key", fmt.Sprintf("model-%d", round), model.ProtocolOpenAIChatCompletion)
		request.finishRound(fmt.Sprintf("error-%d", round))
	}
	if len(request.Rounds) != maxRounds {
		t.Fatalf("rounds = %d, want %d", len(request.Rounds), maxRounds)
	}
	last := request.Rounds[len(request.Rounds)-1]
	if last.Round != 25 || last.Error != "error-25" || last.Sending {
		t.Fatalf("last round = %+v", last)
	}
	foundPrevious := false
	for _, round := range request.Rounds {
		if round.Round == 24 && round.Error == "error-24" {
			foundPrevious = true
		}
	}
	if !foundPrevious {
		t.Fatalf("previous failure was dropped: %+v", request.Rounds)
	}
}

func TestRequestRoundsSnapshotImmutable(t *testing.T) {
	request := &RequestState{}
	request.startRound(func() {}, "ch key", "model", model.ProtocolOpenAIChatCompletion)
	snapshot := request.Rounds
	request.finishRound("boom")
	if snapshot[len(snapshot)-1].Error != "" {
		t.Fatalf("finishRound mutated an existing snapshot")
	}
}
```

Run: `go test ./internal/relay -run TestRequestRounds`

Expected: FAIL，`Rounds` / `maxRounds` 未定义。

- [ ] **Step 2: 增加轮次记录**

`internal/relay/state.go`：

```go
const maxRounds = 20 // 单个请求最多保留的轮次记录。

type RoundRecord struct {
	Round            int            `json:"round"`
	RoundStartedAt   time.Time      `json:"round_started_at"`
	TargetChannelKey string         `json:"target_channel_key"`
	TargetModel      string         `json:"target_model"`
	TargetProtocol   model.Protocol `json:"target_protocol"`
	Sending          bool           `json:"sending"`
	Error            string         `json:"error,omitempty"`
}
```

在 `RequestState` 中增加：

```go
Rounds []RoundRecord `json:"rounds"` // 按发生顺序保留的轮次记录, 最多 maxRounds 条。
```

- [ ] **Step 3: startRound 追加记录**

在 `startRound` 锁内、设置字段之后：

```go
rounds := r.Rounds
if len(rounds) >= maxRounds {
	rounds = append([]RoundRecord(nil), rounds[len(rounds)-maxRounds+1:]...)
} else {
	rounds = append([]RoundRecord(nil), rounds...)
}
r.Rounds = append(rounds, RoundRecord{
	Round:            r.Round,
	RoundStartedAt:   r.RoundStartedAt,
	TargetChannelKey: channelKeyName,
	TargetModel:      modelName,
	TargetProtocol:   protocol,
	Sending:          true,
})
```

- [ ] **Step 4: finishRound 更新最后一轮**

在 `finishRound` 锁内、设置字段之后：

```go
if len(r.Rounds) > 0 {
	rounds := append([]RoundRecord(nil), r.Rounds...)
	last := &rounds[len(rounds)-1]
	last.Sending = false
	last.Error = errText
	r.Rounds = rounds
}
```

- [ ] **Step 5: 验证**

Run: `go test ./internal/relay -run TestRequestRounds`

Expected: PASS。

- [ ] **Step 6: 提交**

```bash
git add internal/relay/state.go internal/relay/state_test.go
git commit -m "fix: :bug: 日志保留每轮失败原因"
```

---

### Task 10: 日志前端渲染轮次历史

**Files:**
- Modify: `web/src/api/log.ts`
- Modify: `web/src/components/modules/log/Item.tsx`
- Modify: `web/src/locales/zh_hans.json`、`zh_hant.json`、`en.json`

**Interfaces:**
- Consumes: `RelayLogOverview.rounds`
- Produces: 详情弹窗完整轮次历史、卡片“已重试 N 次”。

- [ ] **Step 1: 增加类型**

`web/src/api/log.ts`：

```ts
export interface RelayRound {
    round: number;
    round_started_at: string;
    target_channel_key: string;
    target_model: string;
    target_protocol: number;
    sending: boolean;
    error?: string;
}
```

`RelayLogOverview` 增加：

```ts
rounds: RelayRound[];
```

- [ ] **Step 2: 删除前端自行拼接轮次的逻辑**

`Item.tsx`：

- 删除 `ObservedRound` 接口。
- `LogDetail` 参数改为 `{ log, now }`。
- 删除 `rounds` state、`observedRoundKey`、整段 `roundKey` 渲染期更新逻辑。
- 使用：

```ts
const rounds = [...(log.rounds ?? [])].reverse();
const showRounds = log.status === 'running' || (requestFailed && rounds.length > 0);
```

- 轮次渲染字段改为 `round.round`、`round.round_started_at`、`round.target_channel_key`、`round.sending`、`round.error`。

- [ ] **Step 3: 卡片显示最新错误与重试次数**

`LogCardBody`：

- 删除 `displayError`、`errorRounds` 及两个相关 `useEffect`。
- 计算：

```ts
const rounds = log.rounds ?? [];
const latestFailure = [...rounds].reverse().find((round) => round.error);
const visibleError = log.status === 'committed' || log.status === 'success'
    ? ''
    : (log.error || latestFailure?.error || '');
const retryCount = Math.max(0, rounds.length - 1);
```

- 错误横幅沿用 `visibleError`。
- 在模型标题行末尾、状态图标之前增加：

```tsx
{retryCount > 0 && (
    <span className="shrink-0 text-xs text-muted-foreground">
        {t('retryCount', { count: retryCount })}
    </span>
)}
```

- `LogDetail` 调用改为 `<LogDetail log={log} now={now} />`。

- [ ] **Step 4: 翻译**

三份 locale 的 `log.card` 增加：

- zh_hans：`"retryCount": "已重试 {count} 次"`
- zh_hant：`"retryCount": "已重試 {count} 次"`
- en：`"retryCount": "Retried {count} times"`

- [ ] **Step 5: 验证**

Run: `cd web && npm exec --yes pnpm@11.10.0 -- run build`

Expected: PASS。

- [ ] **Step 6: 提交**

```bash
git add web/src/api/log.ts web/src/components/modules/log/Item.tsx web/src/locales
git commit -m "fix: :bug: 日志详情展示每轮失败原因"
```

---

### Task 11: 全量验证与镜像发布

**Files:**
- 无源码修改；仅构建与验证。

**Interfaces:**
- Consumes: 前 10 个任务的成果。

- [ ] **Step 1: 后端全量测试**

Run: `go test ./... && go vet ./...`

Expected: 全部 PASS。

- [ ] **Step 2: 前端构建**

Run: `cd web && npm exec --yes pnpm@11.10.0 -- run build`

Expected: PASS。

- [ ] **Step 3: 交叉编译**

```bash
cd /Users/ulquiola/Codespace/octopus
APP_NAME=octopus
VERSION=v0.13.13
COMMIT="$(git rev-parse --short HEAD)"
LDFLAGS="-X 'github.com/bestruirui/${APP_NAME}/internal/conf.Version=${VERSION}' \
          -X 'github.com/bestruirui/${APP_NAME}/internal/conf.BuildTime=$(TZ='Asia/Shanghai' date +'%F %T %z')' \
          -X 'github.com/bestruirui/${APP_NAME}/internal/conf.Author=bestrui' \
          -X 'github.com/bestruirui/${APP_NAME}/internal/conf.Commit=${COMMIT}' \
          -s -w"
mkdir -p build/docker/linux/amd64 build/docker/linux/arm64
for arch in amd64 arm64; do
  env GOOS=linux GOARCH="$arch" CGO_ENABLED=0 go build -trimpath \
    -o "build/docker/linux/$arch/$APP_NAME" -ldflags="$LDFLAGS" -tags=jsoniter .
done
file build/docker/linux/amd64/octopus build/docker/linux/arm64/octopus
```

Expected: 两个静态 ELF，架构分别为 x86-64 与 ARM aarch64。

- [ ] **Step 4: 生成许可证清单**

```bash
GOFLAGS="-tags=jsoniter" go run github.com/google/go-licenses/v2@v2.0.1 report . \
  --ignore "github.com/bestruirui/octopus" > build/THIRD_PARTY_LICENSES.csv
```

Expected: 退出码 0，CSV 至少 100 行。

- [ ] **Step 5: 构建并推送多架构镜像**

```bash
docker buildx build --builder multiarch \
  --platform linux/amd64,linux/arm64 \
  --provenance=false --sbom=false \
  -f scripts/dockerfile/Dockerfile \
  -t registry.cn-hangzhou.aliyuncs.com/ulquiola/octopus:v0.13.13 \
  -t registry.cn-hangzhou.aliyuncs.com/ulquiola/octopus:latest \
  --push .
```

Expected: 同时推送两个 tag，无 attestation 错误。

- [ ] **Step 6: 验证远端 manifest**

```bash
docker buildx imagetools inspect registry.cn-hangzhou.aliyuncs.com/ulquiola/octopus:latest
```

Expected: `Manifests` 同时包含 `linux/amd64` 与 `linux/arm64`。

- [ ] **Step 7: 运行冒烟测试**

```bash
docker run --rm --platform linux/arm64 --entrypoint /app/octopus \
  registry.cn-hangzhou.aliyuncs.com/ulquiola/octopus:latest version
docker run --rm --platform linux/amd64 --entrypoint /app/octopus \
  registry.cn-hangzhou.aliyuncs.com/ulquiola/octopus:latest version
```

Expected: 两个架构均输出 `Version: v0.13.13`，Commit 为当前 `dev` 分支 HEAD。

- [ ] **Step 8: 汇报结果**

记录镜像 digest、两个平台的 manifest digest，以及测试结果。