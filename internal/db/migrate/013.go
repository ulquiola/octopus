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
