import { useQuery } from '@tanstack/react-query'
import { AlertTriangle, WifiOff } from 'lucide-react'

import { api, APIError } from '@/lib/api'

interface HealthResponse {
  status: string
  version: string
  commit: string
  uptime_s: number
  db_ok: boolean
}

/**
 * Sticky banner under the topbar. Renders only when /health reports
 * the gateway is unhealthy (DB offline) or when the request itself
 * fails (gateway unreachable).
 */
export function HealthBanner() {
  const { data, isError, error } = useQuery<HealthResponse>({
    queryKey: ['health'],
    queryFn: () => api<HealthResponse>('/api/v1/health'),
    refetchInterval: 10_000,
    retry: false,
  })

  // Gateway unreachable.
  if (isError) {
    const status = error instanceof APIError ? error.status : 0
    return (
      <Banner tone="failed">
        <WifiOff className="size-3.5" />
        Gateway unreachable
        {status ? ` (HTTP ${status})` : ''} — events and live data won't
        update until the connection recovers.
      </Banner>
    )
  }

  // DB unhealthy → /health 503 with status='degraded' or db_ok=false.
  if (data && (data.status !== 'ok' || !data.db_ok)) {
    return (
      <Banner tone="failed">
        <AlertTriangle className="size-3.5" />
        Gateway reports{' '}
        <code className="font-mono mx-1">
          status={data.status} db_ok={String(data.db_ok)}
        </code>
        — writes will fail until postgres recovers.
      </Banner>
    )
  }

  return null
}

function Banner({
  tone,
  children,
}: {
  tone: 'failed' | 'idle'
  children: React.ReactNode
}) {
  const cls =
    tone === 'failed'
      ? 'bg-state-failed/15 border-state-failed/30 text-state-failed'
      : 'bg-state-idle/15 border-state-idle/30 text-state-idle'
  return (
    <div
      className={`border-b ${cls} px-4 py-1.5 text-[12px] flex items-center gap-2`}
      role="alert"
    >
      {children}
    </div>
  )
}
