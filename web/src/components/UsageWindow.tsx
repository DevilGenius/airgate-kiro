import { useEffect, useState } from 'react';
import type { AccountSurfaceProps } from '@devilgenius/airgate-theme/plugin';

interface UsageWindowItem {
  key?: string;
  label: string;
  display_label?: string;
  slot?: string;
  group?: string;
  used_percent: number;
  reset_seconds: number;
  reset_at?: string;
}

function getUsageWindows(context: AccountSurfaceProps['context']): UsageWindowItem[] {
  const windows = context?.windows;
  if (!Array.isArray(windows)) return [];
  return windows.filter((item): item is UsageWindowItem => (
    item !== null
    && typeof item === 'object'
    && typeof item.label === 'string'
    && typeof item.used_percent === 'number'
    && typeof item.reset_seconds === 'number'
  ));
}

function formatReset(seconds: number) {
  if (!seconds || seconds <= 0) return '-';
  const d = Math.floor(seconds / 86400);
  const h = Math.floor((seconds % 86400) / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  if (d > 0) return h > 0 ? `${d}d${h}h` : `${d}d`;
  if (h > 0) return m > 0 ? `${h}h${m}m` : `${h}h`;
  return `${m}m`;
}

function useResetTick(enabled: boolean) {
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    if (!enabled) return undefined;
    const timer = window.setInterval(() => setNow(Date.now()), 30_000);
    return () => window.clearInterval(timer);
  }, [enabled]);

  return now;
}

function resolveResetSeconds(w: UsageWindowItem, now: number) {
  if (w.reset_at) {
    const delta = Date.parse(w.reset_at) - now;
    if (Number.isFinite(delta)) return Math.max(0, Math.floor(delta / 1000));
  }
  return w.reset_seconds;
}

function usageColor(pct: number) {
  if (pct < 50) return 'var(--ag-success)';
  if (pct < 80) return 'var(--ag-warning)';
  return 'var(--ag-danger)';
}

export function UsageWindow({ context }: AccountSurfaceProps) {
  const windows = getUsageWindows(context);
  const resetNow = useResetTick(windows.length > 0);
  if (windows.length === 0) return null;

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '0.125rem', minWidth: 0, fontFamily: 'var(--ag-font-mono)' }}>
      {windows.map((w) => {
        const percent = Math.round(w.used_percent);
        const barPercent = Math.max(0, Math.min(100, percent));
        const color = usageColor(w.used_percent);
        const resetText = formatReset(resolveResetSeconds(w, resetNow));
        // core normalizer 会下发 display_label（如 "Cr"）；插件 devserver
        // 直读上游响应时回退到 slot 或原始 label。
        const displayLabel = w.display_label?.trim() || w.slot?.trim() || w.label;
        return (
          <div key={w.key || w.label} style={{ display: 'flex', flexDirection: 'column', gap: '0.125rem', minWidth: 0 }}>
            <span title={w.label} style={{ fontSize: '0.625rem', fontWeight: 600, lineHeight: 1, color: 'var(--ag-text-secondary)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
              {displayLabel}
            </span>
            <div style={{ display: 'grid', gridTemplateColumns: 'minmax(4.625rem, 1fr) 1.375rem 2.75rem', height: '0.75rem', alignItems: 'center', gap: '0.125rem', minWidth: 0 }}>
              <div style={{ height: '0.375rem', minWidth: 0, overflow: 'hidden', borderRadius: '999px', background: 'var(--ag-glass-border)' }}>
                <div style={{ width: `${barPercent}%`, height: '100%', borderRadius: '999px', background: color }} />
              </div>
              <span style={{ width: '100%', minWidth: 0, overflow: 'hidden', textAlign: 'right', fontSize: '0.625rem', fontWeight: 600, lineHeight: 1, fontVariantNumeric: 'tabular-nums', whiteSpace: 'nowrap', color }}>
                {percent}%
              </span>
              <span style={{ display: 'inline-flex', height: '100%', minWidth: 0, alignItems: 'center', justifyContent: 'flex-end', fontSize: '0.625rem', fontWeight: 600, lineHeight: 1, fontVariantNumeric: 'tabular-nums', whiteSpace: 'nowrap', color: 'var(--ag-text-secondary)' }}>
                {resetText}
              </span>
            </div>
          </div>
        );
      })}
    </div>
  );
}
