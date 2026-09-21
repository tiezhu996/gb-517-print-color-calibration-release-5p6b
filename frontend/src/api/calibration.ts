
import { request } from './client';

export type CalibrationStatus = 'pending' | 'passed' | 'failed';

export interface CalibrationRevision {
  id: number;
  version: number;
  status: CalibrationStatus;
  targetDelta: number;
  sample: string;
  retestDueAt: string;
  measuredDelta: number | null;
  result: string;
  evidence: string;
  pressCode: string;
  printRunCode: string;
  quarantineCode: string;
  actor: string;
  requestId: string;
  reason: string;
  createdAt: string;
}

export interface CalibrationRequest {
  id: number;
  code: string;
  name: string;
  status: CalibrationStatus;
  version: number;
  description: string;
  printRunId: number;
  printRunCode: string;
  pressId: number;
  pressCode: string;
  targetDelta: number;
  sample: string;
  retestDueAt: string;
  measuredDelta: number | null;
  result: string;
  evidence: string;
  resolvedBy: string;
  resolvedAt: string | null;
  quarantineCode: string;
  createdAt: string;
  updatedAt: string;
  revisions?: CalibrationRevision[];
}

export interface CalibrationPage {
  items: CalibrationRequest[];
  total: number;
}

export interface CreateCalibrationInput {
  printRunId: number;
  pressId: number;
  targetDelta: number;
  sample: string;
  retestDueAt: string;
  evidence?: string;
  reason: string;
}

export interface ResolveCalibrationInput {
  expectedVersion: number;
  measuredDelta: number;
  evidence?: string;
  reason: string;
}

export async function listCalibrations(params: { printRunId?: number; status?: CalibrationStatus } = {}) {
  const search = new URLSearchParams({ page: '1', pageSize: '100' });
  if (params.printRunId) search.set('printRunId', String(params.printRunId));
  if (params.status) search.set('status', params.status);
  return request<CalibrationRequest[]>(`/calibrations?${search.toString()}`);
}

export async function getCalibration(id: number) {
  return request<CalibrationRequest>(`/calibrations/${id}`);
}

export async function createCalibration(input: CreateCalibrationInput) {
  return request<CalibrationRequest>('/calibrations', { method: 'POST', body: JSON.stringify(input) });
}

export async function resolveCalibration(id: number, input: ResolveCalibrationInput) {
  return request<CalibrationRequest>(`/calibrations/${id}/resolve`, { method: 'POST', body: JSON.stringify(input) });
}
