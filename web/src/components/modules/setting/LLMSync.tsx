import { useEffect, useRef, useState } from 'react';
import { useTranslations } from 'use-intl';
import { Clock, RefreshCw } from 'lucide-react';
import { toast } from 'sonner';
import { Input } from '@/components/ui/input';
import { Button } from '@/components/ui/button';
import { SettingKey, useSetSetting, useSettingList } from '@/api/setting';
import { useLastSyncTime, useSyncChannel } from '@/api/channel';

// SettingLLMSync 管理渠道模型的定时同步间隔与手动同步入口。
export function SettingLLMSync() {
    const t = useTranslations('setting');
    const { data: settings } = useSettingList();
    const setSetting = useSetSetting();
    const syncChannel = useSyncChannel();
    const { data: lastSyncTime } = useLastSyncTime();
    const [syncInterval, setSyncInterval] = useState('');
    const initialSyncInterval = useRef('');

    useEffect(() => {
        const interval = settings?.find((setting) => setting.key === SettingKey.SyncLLMInterval);
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