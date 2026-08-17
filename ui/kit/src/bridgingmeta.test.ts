// bridgingmeta.test.ts — literal pins for bridgingmeta.ts's local-
// demonstration copy (LOCAL_DEMO_CHIP/DEMO_RESULT_*/DEMO_REMOTE_CAPTION/
// FROZEN_SOURCE_NODE/LOCAL_DEMO_FRAMING). The wire-run pinned strings
// (DEMO_MODE_BADGE/BRIDGING_REMOTE_CAPTION/ENGINE_EXHIBIT_FRAMING/
// REFUSAL_EXHIBIT_FRAMING) keep their existing double-assert home,
// BridgingPanel.test.tsx — untouched here. Each string's RENDERED half of
// the double-assert idiom lives in the component that actually shows it
// (DemoChips.test.tsx, FlowMap.test.tsx) once that component exists;
// LOCAL_DEMO_FRAMING's rendered half lands with StepDetail's demonstration
// branch (a later change) — this file only pins the literals.
import { describe, it, expect } from 'vitest';
import {
  DEMO_REMOTE_CAPTION,
  DEMO_REPLAY_FAILURE_NOTE,
  DEMO_RESTORED_VERDICT,
  DEMO_RESULT_CARRY,
  DEMO_RESULT_REFUSAL,
  DEMO_STEP_CLASS_CAPTION,
  DEMO_STEP_LABEL,
  FROZEN_SOURCE_NODE,
  LOCAL_DEMO_CHIP,
  LOCAL_DEMO_FRAMING,
} from './bridgingmeta';

describe('bridgingmeta — local-demonstration pinned strings (literal)', () => {
  it('LOCAL_DEMO_CHIP', () => {
    expect(LOCAL_DEMO_CHIP).toBe('local demonstration — no network');
  });

  it('DEMO_RESULT_REFUSAL', () => {
    expect(DEMO_RESULT_REFUSAL).toBe('Refused as expected');
  });

  it('DEMO_RESULT_CARRY', () => {
    expect(DEMO_RESULT_CARRY).toBe('Restored exactly');
  });

  it('DEMO_REMOTE_CAPTION', () => {
    expect(DEMO_REMOTE_CAPTION).toBe(
      'not involved — this demonstration never left your Smart Gateway',
    );
  });

  it('FROZEN_SOURCE_NODE', () => {
    expect(FROZEN_SOURCE_NODE).toBe('Frozen reference content');
  });

  it('LOCAL_DEMO_FRAMING — the "." separator between ENGINE_EXHIBIT_FRAMING and the closing sentence is load-bearing (the base constant carries no terminal period)', () => {
    expect(LOCAL_DEMO_FRAMING).toBe(
      'engine demonstration over frozen reference content — the same modules your live legs route through. Nothing crossed the network for this run.',
    );
  });

  it('DEMO_STEP_LABEL', () => {
    expect(DEMO_STEP_LABEL).toBe('dtr-questionnaire-response');
  });

  it('DEMO_STEP_CLASS_CAPTION', () => {
    expect(DEMO_STEP_CLASS_CAPTION).toBe('engine demonstration');
  });

  it('DEMO_REPLAY_FAILURE_NOTE', () => {
    expect(DEMO_REPLAY_FAILURE_NOTE).toBe('Replay failed — the exhibit could not run.');
  });

  it('DEMO_RESTORED_VERDICT — the ONE pin XformDiff.tsx and StepDetail.tsx both read', () => {
    expect(DEMO_RESTORED_VERDICT).toBe('0 regions differ — byte-identical');
  });
});
