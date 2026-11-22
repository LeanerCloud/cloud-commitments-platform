import { onThemeChange } from '../lib/chart-theme';
import { dashboardState } from './dashboardState';
import { renderSavingsByService, loadSavingsTrendChart } from '../dashboard';

let stopThemeWatch: (() => void) | null = null;

// One module-lifetime subscription, started when the first chart is drawn.
// Each rebuild is a no-op for a chart that is not on screen.
export function watchTheme(): void {
  stopThemeWatch ??= onThemeChange(() => {
    if (dashboardState.savingsByServiceChart && dashboardState.lastSavingsByServiceArgs) renderSavingsByService(...dashboardState.lastSavingsByServiceArgs);
    if (dashboardState.savingsTrendChart) void loadSavingsTrendChart();
  });
}
