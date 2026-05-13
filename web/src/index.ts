import { AccountForm } from './components/AccountForm';
import { UsageCostDetail } from './components/UsageCostDetail';
import { UsageModelMeta } from './components/UsageModelMeta';
import { UsageWindow } from './components/UsageWindow';
import type { PluginFrontendModule } from '@doudou-start/airgate-theme/plugin';

const plugin: PluginFrontendModule = {
  accountCreate: AccountForm,
  accountEdit: AccountForm,
  accountUsageWindow: UsageWindow,
  usageModelMeta: UsageModelMeta,
  usageCostDetail: UsageCostDetail,
};

export default plugin;
