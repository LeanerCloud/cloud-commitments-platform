import type { Chart } from 'chart.js';
import type { renderSavingsByService } from './byService';

export const dashboardState = {
  // Separate Chart instance for the trend widget so it doesn't conflict with
  // the per-service savings chart instance below.
  savingsTrendChart: null as Chart | null,
  // Chart instance for the per-service savings-range bar chart (issue #765).
  savingsByServiceChart: null as Chart | null,
  // Inputs of the last per-service render, replayed when the color scheme
  // changes (the canvas only picks up new token values on a rebuild, #699).
  lastSavingsByServiceArgs: null as Parameters<typeof renderSavingsByService> | null,
  // Bumped on every upcoming request so a slower, older response cannot
  // overwrite the widget after a newer chip selection rendered (#713).
  upcomingGen: 0,
};
