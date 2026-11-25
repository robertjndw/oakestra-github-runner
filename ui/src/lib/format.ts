/** Compact age such as "45s", "12m", "3h 5m", "2d". */
export function formatAge(from: string | undefined, now: number = Date.now()): string {
  if (!from) return '-';
  const seconds = Math.max(0, Math.floor((now - new Date(from).getTime()) / 1000));
  if (Number.isNaN(seconds)) return '-';
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ${minutes % 60}m`;
  return `${Math.floor(hours / 24)}d`;
}

export function formatDuration(start: string | undefined, end: string | undefined): string {
  if (!start || !end) return '-';
  const seconds = Math.round((new Date(end).getTime() - new Date(start).getTime()) / 1000);
  if (Number.isNaN(seconds) || seconds < 0) return '-';
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ${seconds % 60}s`;
  return `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
}

export function formatTimestamp(value: string | undefined): string {
  if (!value) return '-';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '-';
  return date.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });
}

export function formatMemory(mb: number): string {
  return mb % 1024 === 0 ? `${mb / 1024} GB` : `${mb} MB`;
}
