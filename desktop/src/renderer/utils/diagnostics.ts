import type { CoreStatus, LogDTO, SnapshotDTO } from '@shared/dto'
import { classifyImportEntries } from './imports'

export function buildDiagnosticReport(
  snapshot: SnapshotDTO | undefined,
  coreStatus: CoreStatus,
  logs: LogDTO[],
  refreshError = '',
): string {
  const classified = classifyImportEntries(snapshot)
  return JSON.stringify({
    report: 'tunnelx-desktop-diagnostics',
    generated_at: new Date().toISOString(),
    refresh_error: refreshError,
    core_status: coreStatus,
    client: snapshot ? {
      id: snapshot.id,
      name: snapshot.name,
      server_addr: snapshot.server_addr,
      key_path: snapshot.key_path,
      connection: snapshot.connection,
    } : null,
    registry: {
      raw_count: snapshot?.registry?.length ?? 0,
      available_count: classified.filter(item => item.available).length,
      unavailable_count: classified.filter(item => !item.available).length,
      entries: classified.map(item => ({
        ...item.entry,
        ui_available: item.available,
        ui_filter_reason: item.reason ?? '',
      })),
    },
    tunnels: snapshot?.tunnels ?? [],
    pending_confirmations: snapshot?.pending_confirmations ?? [],
    recent_logs: logs.slice(-100),
  }, null, 2)
}
