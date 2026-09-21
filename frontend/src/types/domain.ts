
export interface DomainRecord {
  id: number;
  code: string;
  name: string;
  status: string;
  version: number;
  description: string;
  facility: string;
  owner: string;
  category: string;
  riskLevel: 'low' | 'medium' | 'high' | 'critical';
  metricValue: number;
  metricUnit: string;
  effectiveAt: string;
  evidence: string;
  relatedCode: string;
  createdAt: string;
  updatedAt: string;
  revisions?: RevisionRecord[];
  latestCalibration?: CalibrationRecord | null;
  calibrations?: CalibrationRecord[];
}

export interface RevisionRecord {
  id: number; version: number; status: string; name: string; metricValue: number;
  metricUnit: string; evidence: string; actor: string; requestId: string; reason: string; createdAt: string;
}

// CalibrationRecord is the batch colour re-calibration closed loop. Pending
// requests block batch release; passed retests clear the gate and failed
// retests hold the batch with an auto-generated quarantine decision.
export interface CalibrationRecord {
  id: number;
  code: string;
  printRunId: number;
  pressUnitId: number;
  pressCode: string;
  pressName: string;
  samples: string;
  targetDelta: number;
  retestDueAt: string;
  status: 'pending' | 'passed' | 'failed';
  version: number;
  measuredDelta: number | null;
  resultNote: string;
  completedAt: string | null;
  completedBy: string;
  createdBy: string;
  requestId: string;
  createdAt: string;
  updatedAt: string;
  deviation?: number | null;
  runStatus?: string;
  quarantineCode?: string;
  quarantineDecisionId?: number;
}

export interface PageMeta { page: number; pageSize: number; total: number }
export interface ApiEnvelope<T> { data: T; error?: string; message?: string; meta?: PageMeta }
export interface UserSession { token: string; username: string; displayName: string; role: string; expiresIn: number }
export interface AuditLog {
  id: number; requestId: string; actor: string; action: string; entityType: string;
  entityId: number; beforeState: string; afterState: string; detail: string; createdAt: string;
}
export interface EntityConfig { key: string; path: string; label: string; statuses: readonly string[] }
