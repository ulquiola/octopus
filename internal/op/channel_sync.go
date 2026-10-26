package op

import (
	"context"
	"fmt"
	"sort"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"gorm.io/gorm"
)

// ChannelModelSyncResult 是一轮模型同步对渠道产生的变更。
type ChannelModelSyncResult struct {
	Added   []string // 本轮新建的自动模型名称。
	Deleted []string // 本轮删除的自动模型名称。
}

type channelModelSyncPlan struct {
	create    []string                             // 需要新建的自动模型。
	deleteIDs []int                                // 需要删除的自动模型主键。
	desired   map[string]map[string]model.Protocol // 模型名 -> 凭据名 -> 协议位, 仅自动模型。
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
	sort.Strings(plan.create)
	for name, channelModel := range autoByName {
		if _, ok := allFetched[name]; !ok {
			plan.deleteIDs = append(plan.deleteIDs, channelModel.ID)
		}
	}
	sort.Ints(plan.deleteIDs)
	for _, keyName := range syncedKeys {
		for _, fetchedModel := range fetched[keyName] {
			if _, ok := manual[fetchedModel.Name]; ok {
				continue
			}
			if fetchedModel.Protocols == 0 {
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

// filterDeletableModelIDs 从待删除模型中剔除仍被未同步凭据引用的模型。
func filterDeletableModelIDs(candidates []int, grants []model.ChannelGrant, unsyncedKeyIDs map[int]struct{}) []int {
	if len(candidates) == 0 || len(unsyncedKeyIDs) == 0 {
		return candidates
	}
	keep := make(map[int]struct{})
	for _, grant := range grants {
		if _, ok := unsyncedKeyIDs[grant.ChannelKeyID]; ok {
			keep[grant.ChannelModelID] = struct{}{}
		}
	}
	filtered := make([]int, 0, len(candidates))
	for _, id := range candidates {
		if _, ok := keep[id]; !ok {
			filtered = append(filtered, id)
		}
	}
	return filtered
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
		syncedKeyID := make(map[int]struct{}, len(syncedKeys))
		for _, keyName := range syncedKeys {
			if id, ok := keyIDByName[keyName]; ok {
				syncedKeyID[id] = struct{}{}
			}
		}
		unsyncedKeyID := make(map[int]struct{}, len(channelKeys))
		for _, channelKey := range channelKeys {
			if _, ok := syncedKeyID[channelKey.ID]; !ok {
				unsyncedKeyID[channelKey.ID] = struct{}{}
			}
		}

		plan := buildChannelModelSyncPlan(existing, fetched, syncedKeys)
		// 未同步成功的凭据可能仍在使用某个模型; 只要该模型在它上面还有授权, 本轮就不删除,
		// 等所有凭据都探测成功后再由后续同步清理。
		if len(plan.deleteIDs) > 0 && len(unsyncedKeyID) > 0 {
			var candidateGrants []model.ChannelGrant
			if err := tx.Where("channel_model_id IN ?", plan.deleteIDs).Find(&candidateGrants).Error; err != nil {
				return fmt.Errorf("failed to load candidate grants: %w", err)
			}
			plan.deleteIDs = filterDeletableModelIDs(plan.deleteIDs, candidateGrants, unsyncedKeyID)
		}
		result.Added = append(result.Added, plan.create...)
		deletedIDs := make(map[int]struct{}, len(plan.deleteIDs))
		for _, id := range plan.deleteIDs {
			deletedIDs[id] = struct{}{}
		}
		for _, channelModel := range existing {
			if _, ok := deletedIDs[channelModel.ID]; ok {
				result.Deleted = append(result.Deleted, channelModel.Name)
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
			if err := tx.Where("channel_model_id IN ?", plan.deleteIDs).
				Delete(&model.ChannelModelStatsDaily{}).Error; err != nil {
				return fmt.Errorf("failed to delete channel model daily stats: %w", err)
			}
			if err := tx.Where("id IN ?", plan.deleteIDs).
				Delete(&model.ChannelModel{}).Error; err != nil {
				return fmt.Errorf("failed to delete channel models: %w", err)
			}
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
		for _, modelID := range autoModelIDs {
			modelName := autoModelNameByID[modelID]
			for keyID := range syncedKeyID {
				keyName := keyNameByID[keyID]
				desired := plan.desired[modelName][keyName]
				key := grantKey{modelID, keyID}
				current, exists := existingByKey[key]
				switch {
				case desired == 0 && exists:
					if err := clearActiveItems(tx, []int{current.ID}); err != nil {
						return err
					}
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
