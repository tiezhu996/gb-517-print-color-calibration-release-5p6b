import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  createCalibration,
  listCalibrations,
  resolveCalibration,
  type CalibrationRequest,
} from '../../api/calibration';
import type { DomainRecord } from '../../types/domain';
import { useAuth } from '../../hooks/useAuth';
import { formatDate } from '../../utils/format';
import { ConfirmDialog } from './ConfirmDialog';

const statusLabel: Record<string, string> = {
  pending: '待复测',
  passed: '复测达标',
  failed: '复测超差',
};

function CalibrationBadge({ item }: { item: CalibrationRequest }) {
  return <span className={`status calibration-status calibration-status--${item.status}`}>{statusLabel[item.status] || item.status}</span>;
}

interface CalibrationPanelProps {
  // When a run is supplied the panel focuses on that run's closed loop;
  // without it the panel lists all requests (overview on proof/release pages).
  run?: DomainRecord;
  // Runs & presses are used to fill the scheduling dialog when no run context
  // exists (batch/proof/release overview mode).
  runs?: DomainRecord[];
  presses?: DomainRecord[];
  reviewer: boolean;
  refreshSignal?: number;
  onChanged?: () => void;
}

interface FormState {
  printRunId: number;
  pressId: number;
  targetDelta: string;
  sample: string;
  retestDueAt: string;
  evidence: string;
  reason: string;
}

const emptyForm: FormState = {
  printRunId: 0, pressId: 0, targetDelta: '2.0', sample: '', retestDueAt: '', evidence: '', reason: '',
};

export function CalibrationPanel({ run, runs, presses, reviewer, refreshSignal, onChanged }: CalibrationPanelProps) {
  const [items, setItems] = useState<CalibrationRequest[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [scheduling, setScheduling] = useState(false);
  const [form, setForm] = useState<FormState>({ ...emptyForm, printRunId: run?.id || 0 });
  const [resolving, setResolving] = useState<CalibrationRequest | null>(null);
  const [resolveMeasured, setResolveMeasured] = useState('');
  const [resolveEvidence, setResolveEvidence] = useState('');
  const { session } = useAuth();

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const result = await listCalibrations(run ? { printRunId: run.id } : {});
      setItems(result.data);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : String(reason));
    } finally {
      setLoading(false);
    }
  }, [run]);

  useEffect(() => { void load(); }, [load, refreshSignal]);

  const proofingRuns = useMemo(() => (runs || []).filter((candidate) => candidate.status === 'proofing'), [runs]);

  const openScheduling = () => {
    setForm({ ...emptyForm, printRunId: run?.id || proofingRuns[0]?.id || 0, retestDueAt: defaultDueAt() });
    setError('');
    setNotice('');
    setScheduling(true);
  };

  const submitSchedule = async () => {
    setError('');
    try {
      await createCalibration({
        printRunId: Number(form.printRunId),
        pressId: Number(form.pressId),
        targetDelta: Number(form.targetDelta),
        sample: form.sample,
        retestDueAt: new Date(form.retestDueAt).toISOString(),
        evidence: form.evidence,
        reason: form.reason,
      });
      setScheduling(false);
      setNotice('复校准申请已登记');
      await load();
      onChanged?.();
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : String(reason));
    }
  };

  const openResolve = (item: CalibrationRequest) => {
    setResolving(item);
    setResolveMeasured(String(item.targetDelta));
    setResolveEvidence('');
    setError('');
    setNotice('');
  };

  const submitResolve = async () => {
    if (!resolving) return;
    setError('');
    const measured = Number(resolveMeasured);
    if (Number.isNaN(measured) || measured < 0) {
      setError('实测色差必须是非负数字');
      return;
    }
    try {
      // The server compares the measured value with the target and derives the
      // final status; the recorded evidence is never rewritten by the button.
      await resolveCalibration(resolving.id, {
        expectedVersion: resolving.version,
        measuredDelta: measured,
        evidence: resolveEvidence || resolving.evidence,
        reason: `复测完成，实测 ΔE ${resolveMeasured}`,
      });
      setResolving(null);
      setNotice('复测结果已回填');
      await load();
      onChanged?.();
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : String(reason));
    }
  };

  const shown = items;
  const canScheduleForRun = !run || run.status === 'proofing';

  return <section className="calibration-panel" aria-label="批次色彩复校准闭环">
    <header className="calibration-header">
      <div><span className="eyebrow">COLOR RECALIBRATION</span><h2>{run ? `批次 ${run.code} 的复校准闭环` : '批次色彩复校准闭环'}</h2></div>
      {reviewer && canScheduleForRun && <button className="link-button" onClick={openScheduling}>发起复校准</button>}
    </header>
    {notice && <div className="calibration-notice">{notice}</div>}
    {error && <div className="alert" role="alert">{error}</div>}
    {loading && <div className="loading">正在加载复校准申请…</div>}
    {!loading && shown.length === 0 && <p className="calibration-empty">暂无复校准申请。批次进入校样后，复核人可登记设备、目标色差、样本和复测期限。</p>}
    {shown.length > 0 && <div className="calibration-list">
      {shown.map((item) => <article key={item.id} className={`calibration-card calibration-card--${item.status}`}>
        <div className="calibration-card-head">
          <strong>{item.code}</strong>
          <CalibrationBadge item={item} />
        </div>
        <dl className="calibration-grid">
          <div><dt>批次</dt><dd>{item.printRunCode}</dd></div>
          <div><dt>设备</dt><dd>{item.pressCode}</dd></div>
          <div><dt>目标 ΔE</dt><dd>{item.targetDelta}</dd></div>
          <div><dt>实测 ΔE</dt><dd className={item.status === 'failed' ? 'delta-failed' : item.status === 'passed' ? 'delta-passed' : ''}>{item.measuredDelta === null || item.measuredDelta === undefined ? '待复测' : item.measuredDelta}</dd></div>
          <div><dt>样本</dt><dd>{item.sample}</dd></div>
          <div><dt>复测期限</dt><dd>{formatDate(item.retestDueAt)}</dd></div>
          <div><dt>偏差</dt><dd>{item.measuredDelta === null || item.measuredDelta === undefined ? '-' : `${item.measuredDelta - item.targetDelta > 0 ? '+' : ''}${(item.measuredDelta - item.targetDelta).toFixed(2)}`}</dd></div>
          <div><dt>最终状态</dt><dd>{statusLabel[item.status] || item.status}{item.quarantineCode ? ` · 隔离 ${item.quarantineCode}` : ''}</dd></div>
        </dl>
        <p className="calibration-evidence">{item.evidence || '未登记证据'}</p>
        <footer>
          <small>{item.resolvedBy ? `${item.resolvedBy} 于 ${formatDate(item.resolvedAt || '')} 回填` : `由 ${item.description.includes('复核人') ? item.description : session?.displayName || '复核人'} 登记 · v${item.version}`}</small>
          {reviewer && item.status === 'pending' && <button className="table-action" onClick={() => openResolve(item)}>回填复测</button>}
        </footer>
      </article>)}
    </div>}

    <ConfirmDialog open={scheduling} title="发起批次色彩复校准" onCancel={() => setScheduling(false)} onConfirm={() => void submitSchedule()}>
      <div className="calibration-form">
        {!run && <label>校样批次
          <select value={form.printRunId} onChange={(event) => setForm({ ...form, printRunId: Number(event.target.value) })}>
            <option value={0}>选择 proofing 批次</option>
            {proofingRuns.map((candidate) => <option key={candidate.id} value={candidate.id}>{candidate.code} · {candidate.name}</option>)}
          </select>
        </label>}
        <label>校准设备
          <select value={form.pressId} onChange={(event) => setForm({ ...form, pressId: Number(event.target.value) })}>
            <option value={0}>选择设备</option>
            {(presses || []).filter((press) => press.status !== 'maintenance').map((press) => <option key={press.id} value={press.id}>{press.code} · {press.name}（{press.status}）</option>)}
          </select>
        </label>
        <label>目标色差 ΔE<input type="number" min="0.1" step="0.1" value={form.targetDelta} onChange={(event) => setForm({ ...form, targetDelta: event.target.value })} /></label>
        <label>复测样本<input value={form.sample} placeholder="如：首件签样 + 三个随机位置" onChange={(event) => setForm({ ...form, sample: event.target.value })} /></label>
        <label>复测期限<input type="datetime-local" value={form.retestDueAt} onChange={(event) => setForm({ ...form, retestDueAt: event.target.value })} /></label>
        <label>证据说明<textarea value={form.evidence} onChange={(event) => setForm({ ...form, evidence: event.target.value })} /></label>
        <label>发起原因<input value={form.reason} placeholder="至少 3 个字符" onChange={(event) => setForm({ ...form, reason: event.target.value })} /></label>
      </div>
    </ConfirmDialog>

    <ConfirmDialog open={Boolean(resolving)} title={`回填复测 · ${resolving?.code || ''}`} onCancel={() => setResolving(null)} onConfirm={() => void submitResolve()}>
      <div className="calibration-form">
        <p>目标 ΔE：<strong>{resolving?.targetDelta}</strong>。确认后由系统按实测值判定：达标才允许批次放行；超差则批次转入待处理并生成隔离决定。</p>
        <label>实测色差 ΔE<input type="number" min="0" step="0.01" value={resolveMeasured} onChange={(event) => setResolveMeasured(event.target.value)} /></label>
        <label>复测证据<textarea value={resolveEvidence} placeholder="分光密度仪读数、复测位置等" onChange={(event) => setResolveEvidence(event.target.value)} /></label>
      </div>
    </ConfirmDialog>
  </section>;
}

function defaultDueAt(): string {
  const date = new Date(Date.now() + 24 * 60 * 60 * 1000);
  date.setSeconds(0, 0);
  const pad = (value: number) => String(value).padStart(2, '0');
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
}
