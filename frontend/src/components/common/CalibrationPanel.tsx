import type { CalibrationRecord, DomainRecord } from '../../types/domain';
import { formatDate } from '../../utils/format';
import { StatusBadge } from './StatusBadge';

const stateLabel: Record<CalibrationRecord['status'], string> = {
  pending: '待复测',
  passed: '复测达标',
  failed: '复测超差',
};

function deltaText(value: number | null | undefined): string {
  return typeof value === 'number' ? value.toFixed(2) : '—';
}

// CalibrationBadge is shared by the batch, proof and release pages.
export function CalibrationBadge({ calibration }: { calibration: CalibrationRecord }) {
  return <span className={`calibration-badge calibration-badge--${calibration.status}`}>{stateLabel[calibration.status]}</span>;
}

// CalibrationPanel renders the latest calibration request, its measured
// deviation and final outcome for any aggregate that references a batch.
export function CalibrationPanel({
  record,
  calibration,
  compact = false,
}: {
  record?: DomainRecord;
  calibration?: CalibrationRecord | null;
  compact?: boolean;
}) {
  const data = calibration ?? record?.latestCalibration;
  if (!data) {
    return compact ? null : (
      <section className="calibration-panel calibration-panel--empty" aria-label="色彩复校准">
        <header><div><span className="eyebrow">RECALIBRATION</span><h2>色彩复校准</h2></div><small>暂无申请</small></header>
        <p className="calibration-empty">批次进入校样后，复核人可发起一次色彩复校准。</p>
      </section>
    );
  }
  const overrun = data.status === 'pending' && new Date(data.retestDueAt).getTime() < Date.now();
  return (
    <section className={`calibration-panel calibration-panel--${data.status}`} aria-label="色彩复校准">
      <header>
        <div>
          <span className="eyebrow">RECALIBRATION</span>
          <h2>色彩复校准 · {data.code}</h2>
        </div>
        <CalibrationBadge calibration={data} />
      </header>
      <dl className="calibration-grid">
        <div><dt>复测设备</dt><dd>{data.pressCode} · {data.pressName}</dd></div>
        <div><dt>目标色差</dt><dd>ΔE ≤ {data.targetDelta.toFixed(2)}</dd></div>
        <div><dt>实测色差</dt><dd className={data.status === 'failed' ? 'calibration-over' : ''}>{deltaText(data.measuredDelta)}</dd></div>
        <div>
          <dt>偏差</dt>
          <dd className={typeof data.deviation === 'number' && data.deviation > 0 ? 'calibration-over' : 'calibration-within'}>
            {typeof data.deviation === 'number' ? `${data.deviation > 0 ? '+' : ''}${data.deviation.toFixed(2)} ΔE` : '待复测'}
          </dd>
        </div>
        <div><dt>复测期限</dt><dd className={overrun ? 'calibration-over' : ''}>{formatDate(data.retestDueAt)}{overrun && '（已逾期）'}</dd></div>
        <div><dt>关联批次</dt><dd>{record?.code ?? `#${data.printRunId}`}{data.runStatus ? ` · ${data.runStatus}` : ''}</dd></div>
        <div className="calibration-samples"><dt>复测样本</dt><dd>{data.samples}</dd></div>
        {data.status !== 'pending' && (
          <div><dt>复测结果</dt><dd>{data.completedBy} · {formatDate(data.completedAt || data.updatedAt)}</dd></div>
        )}
        {data.quarantineCode && (
          <div><dt>隔离决定</dt><dd className="calibration-over">{data.quarantineCode}</dd></div>
        )}
      </dl>
      {data.resultNote && <p className="calibration-note">复测说明：{data.resultNote}</p>}
      <footer>
        <StatusBadge status={data.status} />
        <small>v{data.version} · {data.createdBy} · 请求 {data.requestId}</small>
      </footer>
    </section>
  );
}
