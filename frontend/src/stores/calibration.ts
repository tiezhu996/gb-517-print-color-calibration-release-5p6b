import { create } from 'zustand';
import {
  completeCalibration,
  createCalibration,
  getCalibration,
  listCalibrations,
  type CreateCalibrationInput,
  type CompleteCalibrationInput,
} from '../api/calibration';
import type { CalibrationRecord, PageMeta } from '../types/domain';

interface CalibrationState {
  items: CalibrationRecord[];
  meta: PageMeta;
  loading: boolean;
  error: string;
  load: (query?: { status?: string; printRunId?: number; page?: number; pageSize?: number }) => Promise<void>;
  refreshOne: (id: number) => Promise<CalibrationRecord | null>;
  open: (input: CreateCalibrationInput) => Promise<CalibrationRecord>;
  settle: (id: number, input: CompleteCalibrationInput) => Promise<CalibrationRecord>;
  clearError: () => void;
}

export const useCalibrationStore = create<CalibrationState>((set, get) => ({
  items: [],
  meta: { page: 1, pageSize: 20, total: 0 },
  loading: false,
  error: '',
  load: async (query = {}) => {
    set({ loading: true, error: '' });
    try {
      const result = await listCalibrations({ page: 1, pageSize: 50, ...query });
      set({ items: result.data, meta: result.meta || { page: 1, pageSize: result.data.length, total: result.data.length }, loading: false });
    } catch (error) {
      set({ error: error instanceof Error ? error.message : String(error), loading: false });
    }
  },
  refreshOne: async (id) => {
    try {
      const result = await getCalibration(id);
      return result.data;
    } catch (error) {
      set({ error: error instanceof Error ? error.message : String(error) });
      return null;
    }
  },
  open: async (input) => {
    set({ loading: true, error: '' });
    try {
      const result = await createCalibration(input);
      await get().load();
      set({ loading: false });
      return result.data;
    } catch (error) {
      set({ loading: false, error: error instanceof Error ? error.message : String(error) });
      throw error;
    }
  },
  settle: async (id, input) => {
    set({ loading: true, error: '' });
    try {
      const result = await completeCalibration(id, input);
      await get().load();
      set({ loading: false });
      return result.data;
    } catch (error) {
      set({ loading: false, error: error instanceof Error ? error.message : String(error) });
      throw error;
    }
  },
  clearError: () => set({ error: '' }),
}));
