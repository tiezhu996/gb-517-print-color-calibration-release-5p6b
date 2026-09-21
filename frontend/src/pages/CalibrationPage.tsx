import { useEffect, useMemo, useState } from 'react';
import { useAuth, roleAtLeast } from '../hooks/useAuth';
import { usePagination } from '../hooks/usePagination';
import { useCalibrationStore } from '../stores/calibration';
import { request } from '../api/client';
import { ALL_CALIBRATION_STATE } from '../types/status';
import type { CalibrationRecord, DomainRecord } from '../types/domain';
import { formatDate } from '../utils/format';
import { MetricCard } from '../components/common/MetricCard';
import { EmptyState } from '../components/common/EmptyState';
import { UiButton } from '../components/common/UiButton';
import { ConfirmDialog } from '../components/common/ConfirmDialog';
import { CalibrationBadge, CalibrationPanel } from '../components/common/CalibrationPanel';

interface ProofingRun extends DomainRecord {}

const DEFAULT_DEADLINE = () => new Date(Date.now() + 48 * 60 * 60 * 1000).toISOString().slice(0, 16);

export default function CalibrationPage() {
  const { session } = useAuth();
  const canReview = roleAtLeast(session?.role, 'reviewer');
  const { items, meta, loading, error, load, open, settle } = useCalibrationStore();
  const [stateFilter, setStateFilter] = useState('');
  const [showCreate, setShowCreate] = useState(false);
  const [completing, setCompleting] = useState<CalibrationRecord | null>(null);
  const [detail, setDetail] = useState<CalibrationRecord | null>(null);
  const [runs, setRuns] = useState<ProofingRun[]>([]);
  const [presses, setPresses] = useState<DomainRecord[]>([]);
  const [formError, setFormError] = useState('');

  const [runId, setRunId] = useState<number | ''>('');
  const [pressId, setPressId] = useState<number | ''>('');
  const [targetDelta, setTargetDelta] = useState('2.0');
  const [samples, setSamples] = useState('青/品红/黄 三色偏色条 + 灰平衡抽测');
  const [retestDueAt, setRetestDueAt] = useState(DEFAULT_DEADLINE());

  const [measuredDelta, setMeasuredDelta] = useState('');
  const [result, setResult] = useState<'passed' | 'failed'>('passed');
  const [resultNote, setResultNote] = useState('');

  const { page, pages, setPage, previous, next } = usePagination(meta.total);
  const proofingRuns = useMemo(() => runs.filter((run) => run.status === 'proofing'), [runs]);
  const availablePresses = useMemo(() => presses.filter((press) => press.status !== 'maintenance'), [presses]);
  const pendingCount = items.filter((item) => item.status === 'pending').length;
  const failedCount = items.filter((item) => item.status === 'failed').length;

  useEffect(() => {
    void load({ status: stateFilter || undefined, page, pageSize: 20 });
  }, [load, stateFilter, page]);

  useEffect(() => {
    if (!canReview) return;
    void request<DomainRecord[]>('/runs?page=1&pageSize=100').then((response) => setRuns(response.data));
    void request<DomainRecord[]>('/presses?page=1&pageSize=100').then((response) => setPresses(response.data));
  }, [canReview]);

  const resetCreateForm = () => {
    setRunId(''); setPressId(''); setTargetDelta('2.0');
    setSamples('青/品红/黄 三色偏色条 + 灰平衡抽测'); setRetestDueAt(DEFAULT_DEADLINE()); setFormError('');
  };

  const submitCreate = async () => {
    setFormError('');
    if (!runId || !pressId) { setFormError('请选择校样批次和复测设备'); return; }
    const target = Number(targetDelta);
    if (!Number.isFinite(target) || target <= 0) { setFormError('目标色差必须为正数'); return; }
    const due = new Date(retestDueAt);
    if (!(due.getTime() > Date.now())) { setFormError('复测期限必须晚于当前时间'); return; }
    try {
      await open({ printRunId: Number(runId), pressUnitId: Number(pressId), targetDelta: target, samples, retestDueAt: due.toISOString() });
      setShowCreate(false);
      resetCreateForm();
    } catch {
      /* store surfaces the message */
    }
  };

  const openCreate = () => {
    resetCreateForm();
    setShowCreate(true);
  };

  const openComplete = (item: CalibrationRecord) => {
    setCompleting(item);
    setMeasuredDelta(String(item.targetDelta));
    setResult('passed');
    setResultNote('');
    setFormError('');
  };

  const submitComplete = async () => {
    if (!completing) return;
    setFormError('');
    const measured = Number(measuredDelta);
    if (!Number.isFinite(measured) || measured < 0) { setFormError('实测色差必须为非负数'); return; }
    const derived = measured > completing.targetDelta ? 'failed' : 'passed';
    if (derived !== result) { setFormError(`按目标 ΔE ${completing.targetDelta.toFixed(2)} 判定应为「${derived === 'failed' ? '超差' : '达标'}」，结果与实测不一致`); return; }
    try {
      const updated = await settle(completing.id, { expectedVersion: completing.version, measuredDelta: measured, result, resultNote });
      setCompleting(null);
      setDetail(updated);
    } catch {
      /* store surfaces the message */
    }
  };

  const viewDetail = async (item: CalibrationRecord) => {
    const refreshed = await useCalibrationStore.getState().refreshOne(item.id);
    setDetail(refreshed ?? item);
  };

  return <main className="workspace calibration-workspace">
    <header className="page-header">
      <div><p className="eyebrow">批次色彩复校准闭环</p><h1>色彩复校准</h1><p>登记复测设备、目标色差、样本和期限；复测达标解除放行限制，超差自动转待处理并隔离。</p></div>
      {canReview && <UiButton onClick={openCreate}>发起校准</UiButton>}
    </header>
    <section className="metrics">
      <MetricCard label="申请总数" value={meta.total} detail="当前筛选范围" />
      <MetricCard label="待复测" value={pendingCount} detail="占用批次放行闸门" />
      <MetricCard label="复测超差" value={failedCount} detail="已转待处理并隔离" />
    </section>
    <section className="toolbar">
      <label>状态
        <select value={stateFilter} onChange={(event) => { setPage(1); setStateFilter(event.target.value); }} aria-label="按状态筛选">
          <option value="">全部状态</option>
          {ALL_CALIBRATION_STATE.map((state) => <option key={state} value={state}>{state === 'pending' ? '待复测' : state === 'passed' ? '复测达标' : '复测超差'}</option>)}
        </select>
      </label>
      <button className="link-button" onClick={() => { setStateFilter(''); setPage(1); }}>重置</button>
    </section>
    {error && <div className="alert" role="alert">{error}</div>}
    <section className="table-shell" aria-busy={loading}>
      <table>
        <thead><tr><th>申请编号</th><th>批次</th><th>设备</th><th>目标/实测</th><th>偏差</th><th>复测期限</th><th>状态</th><th>操作</th></tr></thead>
        <tbody>
          {items.map((item) => (
            <tr key={item.id}>
              <td><strong>{item.code}</strong><small>{item.createdBy} · v{item.version}</small></td>
              <td>#{item.printRunId}{item.runStatus ? <small>{item.runStatus}</small> : null}</td>
              <td>{item.pressCode}<small>{item.pressName}</small></td>
              <td>ΔE {item.targetDelta.toFixed(2)} / {typeof item.measuredDelta === 'number' ? item.measuredDelta.toFixed(2) : '—'}</td>
              <td className={typeof item.deviation === 'number' && item.deviation > 0 ? 'calibration-over' : ''}>{typeof item.deviation === 'number' ? `${item.deviation > 0 ? '+' : ''}${item.deviation.toFixed(2)}` : '—'}</td>
              <td>{formatDate(item.retestDueAt)}</td>
              <td><CalibrationBadge calibration={item} /></td>
              <td>
                <button className="table-action" onClick={() => void viewDetail(item)}>查看</button>
                {canReview && item.status === 'pending' && <button className="table-action" onClick={() => openComplete(item)}>回填复测</button>}
              </td>
            </tr>
          ))}
          {!items.length && !loading && <tr><td colSpan={8}><EmptyState title="没有复校准申请" detail="复核人可对校样中的批次发起一次校准" /></td></tr>}
        </tbody>
      </table>
      {loading && <div className="loading">正在同步复校准数据…</div>}
    </section>
    <footer className="pagination"><button onClick={previous} disabled={page <= 1}>上一页</button><span>第 {page} / {pages} 页</span><button onClick={next} disabled={page >= pages}>下一页</button></footer>

    <ConfirmDialog open={showCreate} title="发起批次色彩复校准" onCancel={() => setShowCreate(false)} onConfirm={() => void submitCreate()}>
      {formError && <div className="alert">{formError}</div>}
      <div className="calibration-form">
        <label>校样批次（同批次仅允许一条待处理申请）
          <select value={runId} onChange={(event) => setRunId(event.target.value ? Number(event.target.value) : '')}>
            <option value="">请选择 proofing 批次</option>
            {proofingRuns.map((run) => <option key={run.id} value={run.id}>{run.code} · {run.name}{run.latestCalibration?.status === 'pending' ? '（已有待处理申请）' : ''}</option>)}
          </select>
        </label>
        <label>复测设备（维护中不可用）
          <select value={pressId} onChange={(event) => setPressId(event.target.value ? Number(event.target.value) : '')}>
            <option value="">请选择设备</option>
            {availablePresses.map((press) => <option key={press.id} value={press.id}>{press.code} · {press.name}（{press.status}）</option>)}
          </select>
        </label>
        <label>目标色差 ΔE<input type="number" min="0" step="0.1" value={targetDelta} onChange={(event) => setTargetDelta(event.target.value)} /></label>
        <label>复测样本<textarea rows={3} value={samples} onChange={(event) => setSamples(event.target.value)} /></label>
        <label>复测期限<input type="datetime-local" value={retestDueAt} onChange={(event) => setRetestDueAt(event.target.value)} /></label>
      </div>
    </ConfirmDialog>

    <ConfirmDialog open={Boolean(completing)} title={`回填复测结果 · ${completing?.code ?? ''}`} onCancel={() => setCompleting(null)} onConfirm={() => void submitComplete()}>
      {formError && <div className="alert">{formError}</div>}
      <div className="calibration-form">
        <p>目标色差 ΔE ≤ {completing?.targetDelta.toFixed(2)}；实测不大于目标判定达标，批次才允许放行。复测结果一旦提交即锁定，无法覆盖。</p>
        <label>实测色差 ΔE<input type="number" min="0" step="0.1" value={measuredDelta} onChange={(event) => setMeasuredDelta(event.target.value)} /></label>
        <label>判定结果
          <select value={result} onChange={(event) => setResult(event.target.value as 'passed' | 'failed')}>
            <option value="passed">达标</option>
            <option value="failed">超差（批次转待处理并隔离）</option>
          </select>
        </label>
        <label>复测说明<textarea rows={3} value={resultNote} onChange={(event) => setResultNote(event.target.value)} placeholder="通道、样本与处置说明" /></label>
      </div>
    </ConfirmDialog>

    {detail && <ConfirmDialog open={Boolean(detail)} title={`复校准详情 · ${detail.code}`} onCancel={() => setDetail(null)} onConfirm={() => setDetail(null)}>
      <CalibrationPanel calibration={detail} />
    </ConfirmDialog>}
  </main>;
}
