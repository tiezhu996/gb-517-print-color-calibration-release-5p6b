import { useCallback, useEffect, useState } from 'react';
import { request } from '../../api/client';
import type { DomainRecord } from '../../types/domain';
import { roleAtLeast, useAuth } from '../../hooks/useAuth';
import { CalibrationPanel } from './CalibrationPanel';

// CalibrationSection loads the supporting run/press catalogues once and hosts
// the closed-loop panel. In run context it filters to that run; the proof and
// release pages show the full loop and resolve a batch via relatedCode.
export function CalibrationSection({ run, refreshSignal, onChanged }: { run?: DomainRecord; refreshSignal?: number; onChanged?: () => void }) {
  const { session } = useAuth();
  const reviewer = roleAtLeast(session?.role, 'reviewer');
  const [runs, setRuns] = useState<DomainRecord[]>([]);
  const [presses, setPresses] = useState<DomainRecord[]>([]);

  const loadCatalogues = useCallback(async () => {
    try {
      const [runResult, pressResult] = await Promise.all([
        request<DomainRecord[]>('/runs?page=1&pageSize=100'),
        request<DomainRecord[]>('/presses?page=1&pageSize=100'),
      ]);
      setRuns(runResult.data);
      setPresses(pressResult.data);
    } catch {
      // Catalogue failure must not block the pages; the panel keeps its own
      // error surface for write operations.
    }
  }, []);

  useEffect(() => { void loadCatalogues(); }, [loadCatalogues, refreshSignal]);

  return <CalibrationPanel
    run={run}
    runs={runs}
    presses={presses}
    reviewer={reviewer}
    refreshSignal={refreshSignal}
    onChanged={() => { void loadCatalogues(); onChanged?.(); }}
  />;
}
