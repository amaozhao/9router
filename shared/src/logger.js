// Minimal structured logger. Zero deps to keep cold-start fast.
// In production, swap for pino with the same API surface.

const LEVELS = { trace: 10, debug: 20, info: 30, warn: 40, error: 50, fatal: 60 };

function getLevel() {
  const env = (process.env.LOG_LEVEL || 'info').toLowerCase();
  return LEVELS[env] ?? LEVELS.info;
}

const activeLevel = getLevel();

function emit(level, obj, msg) {
  if (LEVELS[level] < activeLevel) return;
  const record = {
    ts: new Date().toISOString(),
    level,
    msg: msg ?? (typeof obj === 'string' ? obj : undefined),
    ...(typeof obj === 'object' && obj !== null ? obj : {}),
  };
  if (typeof obj === 'string' && msg === undefined) record.msg = obj;
  const line = JSON.stringify(record);
  if (LEVELS[level] >= LEVELS.error) process.stderr.write(line + '\n');
  else process.stdout.write(line + '\n');
}

export const logger = {
  trace: (obj, msg) => emit('trace', obj, msg),
  debug: (obj, msg) => emit('debug', obj, msg),
  info:  (obj, msg) => emit('info', obj, msg),
  warn:  (obj, msg) => emit('warn', obj, msg),
  error: (obj, msg) => emit('error', obj, msg),
  fatal: (obj, msg) => emit('fatal', obj, msg),
  child: (bindings) => ({
    trace: (o, m) => emit('trace', { ...bindings, ...(typeof o === 'object' ? o : { msg: o }) }, m),
    debug: (o, m) => emit('debug', { ...bindings, ...(typeof o === 'object' ? o : { msg: o }) }, m),
    info:  (o, m) => emit('info',  { ...bindings, ...(typeof o === 'object' ? o : { msg: o }) }, m),
    warn:  (o, m) => emit('warn',  { ...bindings, ...(typeof o === 'object' ? o : { msg: o }) }, m),
    error: (o, m) => emit('error', { ...bindings, ...(typeof o === 'object' ? o : { msg: o }) }, m),
    fatal: (o, m) => emit('fatal', { ...bindings, ...(typeof o === 'object' ? o : { msg: o }) }, m),
  }),
};
