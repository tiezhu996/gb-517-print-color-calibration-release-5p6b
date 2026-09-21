
import { request } from './client';
import type { CalibrationRecord, PageMeta } from '../types/domain';

export interface CalibrationQuery {
  page?: number;
  pageSize?: number;
  status?: string;
  printRunId?: number;
}

export async function listCalibrations(query: CalibrationQuery = {}) {
  const params = new URLSearchParams();
  if (query.page) params.set('page', String(query.page));
  if (query.pageSize) params.set('pageSize', String(query.pageSize));
  if (query.status) params.set('status', query.status);
  if (query.printRunId) params.set('printRunId', String(query.printRunId));
  return request<CalibrationRecord[]>(`/calibrations?${params.toString()}`);
}

export async function getCalibration(id: number) {
  return request<CalibrationRecord>(`/calibrations/${id}`);
}

export interface CreateCalibrationInput {
  printRunId: number;
  pressUnitId: number;
  targetDelta: number;
  samples: string;
  retestDueAt: string;
}

export async function createCalibration(input: CreateCalibrationInput) {
  return request<CalibrationRecord>('/calibrations', { method: 'POST', body: JSON.stringify(input) });
}

export interface CompleteCalibrationInput {
  expectedVersion: number;
  measuredDelta: number;
  result: 'passed' | 'failed';
  resultNote?: string;
}

export async function completeCalibration(id: number, input: CompleteCalibrationInput) {
  return request<CalibrationRecord>(`/calibrations/${id}/complete`, { method: 'POST', body: JSON.stringify(input) });
}

export type { CalibrationRecord, PageMeta };
