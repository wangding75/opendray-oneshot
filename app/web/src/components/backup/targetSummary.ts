// targetSummary — human-readable one-liner for backup target table rows.
// Extracted from TargetEditor.tsx to satisfy react-refresh/only-export-components
// (TargetEditor exports a React component; this file is non-component only).

import type { TargetSpec } from '@/lib/backup'

export function targetSummary(t: TargetSpec): string {
  if (t.kind === 'local') {
    return String(t.config?.root ?? '(default local dir)')
  }
  if (t.kind === 'smb') {
    const host = t.config?.host
    const share = t.config?.share
    const user = t.config?.user
    const prefix = t.config?.path_prefix
    return `//${host ?? '?'}/${share ?? '?'} as ${user ?? '?'}${prefix ? ` → ${prefix}/` : ''}`
  }
  if (t.kind === 's3') {
    const ep = t.config?.endpoint
    const bucket = t.config?.bucket
    const prefix = t.config?.path_prefix
    return `s3://${bucket ?? '?'}@${ep ?? '?'}${prefix ? `/${prefix}` : ''}`
  }
  if (t.kind === 'webdav') {
    const url = t.config?.base_url
    const user = t.config?.user
    const prefix = t.config?.path_prefix
    return `${url ?? '?'} as ${user ?? '?'}${prefix ? ` → ${prefix}/` : ''}`
  }
  if (t.kind === 'sftp') {
    const host = t.config?.host
    const user = t.config?.user
    const prefix = t.config?.path_prefix
    return `${user ?? '?'}@${host ?? '?'}${prefix ? `:${prefix}` : ''}`
  }
  if (t.kind === 'rclone') {
    const remote = t.config?.remote
    const prefix = t.config?.path_prefix
    return `rclone:${remote ?? '?'}${prefix ? `:${prefix}` : ''}`
  }
  return JSON.stringify(t.config)
}
