import { escapeHtmlAttr } from '../utils';
import { BackendPlan } from './planModel';

// Score bands for the per-plan health badge (issue #340 follow-up). Green
// >= 80 (healthy), amber 50-79 (needs attention), red < 50 (action needed).
// Reuses the shared .badge tones (badge-success/badge-warning/
// badge-danger) already used elsewhere on this card rather than introducing
// new CSS.
const HEALTH_SCORE_GOOD_THRESHOLD = 80;
const HEALTH_SCORE_WARN_THRESHOLD = 50;

// Inclusive bounds of the score the API contracts to send (openapi.yaml
// PlanWithHealth.health_score: minimum 0, maximum 100). Anything outside is
// treated as unusable rather than banded.
const HEALTH_SCORE_MIN = 0;
const HEALTH_SCORE_MAX = 100;

function healthBadgeClass(score: number): string {
  if (score >= HEALTH_SCORE_GOOD_THRESHOLD) return 'badge-success';
  if (score >= HEALTH_SCORE_WARN_THRESHOLD) return 'badge-warning';
  return 'badge-danger';
}

// healthBadgeHtml renders the per-plan health-score badge, distinguishing
// three states the API can report:
//
//   - field absent: an older API response served during a partial deploy.
//     Render nothing so the card still looks intentional.
//   - field null: the backend could not compute the score (execution counts
//     unavailable). Render an explicit "unknown" badge -- never a stand-in
//     number, which an operator could not tell apart from a measured score.
//   - a number within the contract's 0-100 range: the score, banded into the
//     green/amber/red palette.
//   - anything else (NaN, Infinity, out of range, a non-number off the wire):
//     also "unknown". A score outside 0-100 violates the range the API
//     declares, so it is no more trustworthy than a NaN, and banding it would
//     paint -1 red and 101 green as though both were measured. Silently
//     dropping the badge instead would read as "this deploy predates the
//     feature".
//
// The tooltip enumerates every penalty factor so a bad score is actionable
// instead of opaque; every factor note is escaped since it renders inside an
// HTML attribute (title) via innerHTML (feedback_innerhtml_xss: all
// API-sourced values must be escaped here even though the backend generates
// these notes itself).
export function healthBadgeHtml(plan: BackendPlan): string {
  if (plan.health_score === undefined) return '';
  // The typeof/isFinite check is defensive: the type says number, but this
  // value comes off the wire and is interpolated into innerHTML below.
  if (plan.health_score === null
      || typeof plan.health_score !== 'number'
      || !Number.isFinite(plan.health_score)
      || plan.health_score < HEALTH_SCORE_MIN
      || plan.health_score > HEALTH_SCORE_MAX) {
    // Neutral wording on purpose: null means the backend could not read the
    // execution history, while a non-finite value means the number itself
    // arrived unusable. Naming only the first would send an operator
    // chasing a database problem that may not exist.
    return `<span class="badge badge-muted" title="${escapeHtmlAttr('Plan health is unavailable: this plan\'s score could not be determined.')}">Health: unknown</span>`;
  }
  const factors = plan.health_factors || [];
  const factorLines = factors.length > 0
    ? factors.map(f => `-${f.penalty}: ${f.note}`)
    : ['No issues detected'];
  const tooltip = [`Plan health: ${plan.health_score}/100`, ...factorLines].join('\n');
  return `<span class="badge ${healthBadgeClass(plan.health_score)}" title="${escapeHtmlAttr(tooltip)}">Health: ${plan.health_score}</span>`;
}
