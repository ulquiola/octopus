package helper

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"sync"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/rhttp"
	"github.com/dlclark/regexp2"
)

// FilterError 表示渠道或全局模型过滤表达式无法编译, 调用方应作为请求错误返回。
type FilterError struct {
	Err error
}

func (e *FilterError) Error() string { return e.Err.Error() }
func (e *FilterError) Unwrap() error { return e.Err }

// FetchModels 按渠道配置与凭据同时探测 OpenAI 与 Anthropic 两侧的模型列表。
// OpenAI 侧记为 Chat, Anthropic 侧记为 Message; 两侧都失败才返回错误。
func FetchModels(ctx context.Context, config model.ChannelConfig, key string) ([]model.ChannelFetchModel, error) {
	config.BaseURL = strings.TrimSpace(config.BaseURL)
	config.ChannelProxy = strings.TrimSpace(config.ChannelProxy)
	config.MatchRegex = strings.TrimSpace(config.MatchRegex)
	if config.BaseURL == "" {
		return nil, fmt.Errorf("channel base url is required")
	}

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
		// 渠道专用代理的客户端不再共享, 探测完就得关掉空闲连接。
		return client, client.CloseIdleConnections, nil
	}
}

func compileModelFilters(matchRegex string) (*regexp2.Regexp, *regexp2.Regexp, error) {
	var re *regexp2.Regexp
	var err error
	if matchRegex != "" {
		if re, err = regexp2.Compile(matchRegex, regexp2.ECMAScript); err != nil {
			return nil, nil, &FilterError{Err: err}
		}
	}
	// 全局过滤由设置页维护, 读取失败按不过滤处理: 启动初始化会补齐默认值。
	globalFilter, _ := op.SettingGetString(model.SettingKeyModelFilter)
	var reGlobal *regexp2.Regexp
	if globalFilter != "" {
		if reGlobal, err = regexp2.Compile(globalFilter, regexp2.ECMAScript); err != nil {
			return nil, nil, &FilterError{Err: err}
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
// 例如 /v1/chat/completions 与 /v1/messages 都得到 /v1/models, /chat/completions 得到 /models。
func modelsURL(baseURL, protocolPath string) string {
	parent := path.Dir(strings.TrimRight(protocolPath, "/"))
	// Anthropic 的 /v1/messages 只有一层, 父级即 /v1; Chat 的 /v1/chat/completions 需要再上一层。
	if strings.HasSuffix(parent, "/chat") {
		parent = path.Dir(parent)
	}
	if parent == "." || parent == "/" {
		parent = ""
	}
	return strings.TrimRight(baseURL, "/") + parent + "/models"
}

// refer: https://platform.openai.com/docs/api-reference/models/list
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

// refer: https://platform.claude.com/docs
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
		// 分页时每轮都会新建响应, 必须当轮读完即关; 用 defer 会攒到整个函数返回才释放。
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

// decodeModelList 关闭响应并把响应体解成模型列表; 非 2xx 时按上游错误返回。
// 两侧解析流程一致, 只有目标结构不同, 故用类型参数收敛; 分页调用要求当轮读完即关, 关闭点放在此处最稳。
func decodeModelList[T any](response *http.Response) (T, error) {
	defer response.Body.Close()
	var result T
	// 上游报错时响应体常是能被正常解码的 JSON, 若不先拦下, 模型列表会解成空列表并当作成功;
	// 响应体截断到 512 字节: 部分上游在鉴权失败时返回整页 HTML, 全文带到界面上无用。
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