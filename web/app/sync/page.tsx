'use client';

import { useCallback, useEffect, useState } from 'react';

interface Voucher {
  id: string;
  type: string;
  status: 'pending' | 'pushed' | 'failed';
  attempts: number;
  last_error: string;
  client_ref: string;
  external_ref: string;
  created_at: string;
  updated_at?: string;
}

const API_BASE = process.env.NEXT_PUBLIC_API_BASE ?? 'http://localhost:8080';

export default function Sync() {
  const [items, setItems] = useState<Voucher[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState('');

  const load = useCallback(async () => {
    try {
      const res = await fetch(`${API_BASE}/vouchers`, { cache: 'no-store' });
      if (!res.ok) {
        throw new Error(`Server returned ${res.status}: Failed to load vouchers`);
      }
      const data = await res.json();
      setItems(Array.isArray(data) ? data : []);
      setError('');
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Could not load vouchers');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
    const interval = setInterval(load, 4000);
    return () => clearInterval(interval);
  }, [load]);

  async function retry(id: string) {
    if (busy) return;
    setBusy(id);
    setError('');
    try {
      const res = await fetch(`${API_BASE}/vouchers/${id}/retry`, {
        method: 'POST',
      });
      if (!res.ok) {
        const body = await res.json().catch(() => ({}));
        throw new Error(body.error ?? `Retry rejected (${res.status})`);
      }
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Retry failed');
    } finally {
      setBusy('');
    }
  }

  const columns: { status: 'pending' | 'pushed' | 'failed'; label: string }[] = [
    { status: 'pending', label: 'Pending' },
    { status: 'pushed', label: 'Pushed' },
    { status: 'failed', label: 'Failed' },
  ];

  return (
    <main>
      <header>
        <p className="eyebrow">Sutra Mini</p>
        <h1>Accounting sync</h1>
        <p>Voucher delivery status from job-work receipts. Auto-refreshes every 4 seconds.</p>
      </header>

      {error && (
        <div role="alert" className="error" aria-live="polite">
          <strong>Error: </strong> {error}
        </div>
      )}

      {loading && items.length === 0 && (
        <p className="loading-state" aria-live="polite">
          Loading vouchers...
        </p>
      )}

      <section aria-label="Voucher groups">
        {columns.map(({ status, label }) => {
          const groupItems = items.filter((x) => x.status === status);
          return (
            <div className="column" key={status}>
              <h2>
                {label}
                <span aria-label={`${groupItems.length} vouchers in ${label}`}>{groupItems.length}</span>
              </h2>

              {groupItems.map((v) => (
                <article key={v.id} tabIndex={0} aria-label={`Voucher ${v.client_ref}`}>
                  <code>{v.client_ref}</code>
                  <dl>
                    <div>
                      <dt>Attempts</dt>
                      <dd>{v.attempts}</dd>
                    </div>
                    {v.external_ref && (
                      <div>
                        <dt>External Ref</dt>
                        <dd>{v.external_ref}</dd>
                      </div>
                    )}
                  </dl>

                  {v.last_error && <p className="reason">Reason: {v.last_error}</p>}

                  {status === 'failed' && (
                    <button
                      type="button"
                      disabled={busy === v.id}
                      onClick={() => retry(v.id)}
                      aria-label={`Retry voucher ${v.client_ref}`}
                    >
                      {busy === v.id ? 'Retrying…' : 'Retry'}
                    </button>
                  )}
                </article>
              ))}

              {groupItems.length === 0 && !loading && (
                <p className="empty">No {label.toLowerCase()} vouchers.</p>
              )}
            </div>
          );
        })}
      </section>
    </main>
  );
}
