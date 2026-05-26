// Worker process — runs background jobs.
// Current jobs:
//   - usageAggregator (per-minute roll-up into usage_summaries)
// Future jobs (hook here):
//   - tokenRefresher (OAuth refresh for connections with auth_type='oauth')
//   - billingExporter (daily/monthly Stripe push)

import { logger, closeDb, closeRedis } from '@9router-cloud/shared';
import { scheduleAggregator } from './jobs/usageAggregator.js';
import { scheduleRefresher, registerRefresher } from './jobs/tokenRefresher.js';
import { refreshOpenAiOAuth } from './jobs/refreshers/openaiOAuth.js';
import { refreshClaudeOAuth } from './jobs/refreshers/claudeOAuth.js';

const log = logger.child({ svc: 'worker' });

// Register OAuth refresher plugins. Add more providers here as they are wired.
registerRefresher('openai',   refreshOpenAiOAuth);
registerRefresher('codex',    refreshOpenAiOAuth);
registerRefresher('cursor',   refreshOpenAiOAuth);
registerRefresher('claude',   refreshClaudeOAuth);

log.info('starting worker');
const handles = [];
handles.push(scheduleAggregator(60_000));
handles.push(scheduleRefresher(30_000));

const shutdown = async (signal) => {
  log.info({ signal }, 'shutting down');
  for (const h of handles) clearInterval(h);
  await closeDb(); await closeRedis();
  process.exit(0);
};
process.on('SIGINT', () => shutdown('SIGINT'));
process.on('SIGTERM', () => shutdown('SIGTERM'));
