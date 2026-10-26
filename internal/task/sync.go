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
	syncModelsMu         sync.Mutex   // 保证同一时间只有一个模型同步任务运行。
	lastSyncModelsTimeMu sync.RWMutex // 最近同步时间的读写锁。
	lastSyncModelsTime   = time.Now() // 最近一次模型同步任务结束时间。
)

// SyncModelsTask 同步所有开启自动同步的渠道模型。
// 每把启用的 Key 分别探测; 探测失败的 Key 不修改其授权, 单个渠道失败不影响其他渠道。
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

	var firstErr error
	for _, channel := range op.ChannelAutoSyncList() {
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

// addSyncedModelPrices 为新增模型补价格记录; 渠道模型保留大小写, 价格表按小写主键去重。
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

// GetLastSyncModelsTime 返回最近一次模型同步任务结束时间。
func GetLastSyncModelsTime() time.Time {
	lastSyncModelsTimeMu.RLock()
	defer lastSyncModelsTimeMu.RUnlock()
	return lastSyncModelsTime
}
