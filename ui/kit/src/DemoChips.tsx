// DemoChips.tsx — the local-demonstration species pills. LocalDemoChip
// marks every demonstration run's species (dashed, neutral — this run
// never touched the network; a sibling of StatusChip, not a widened
// version of it). DemoResultChip carries the demonstration's own
// kind-keyed verdict sentence on StatusChip's pass-green tick treatment
// (rendered only for the one state a demonstration record can have —
// see kit/kitd/bridging.go's emitDemoRun, called from the success path
// only, so there is no "fail" variant to build).
import type { JSX } from 'react';
import { TickIcon } from './StatusChip';
import { DEMO_RESULT_CARRY, DEMO_RESULT_REFUSAL, LOCAL_DEMO_CHIP } from './bridgingmeta';

export function LocalDemoChip(): JSX.Element {
  return <span className="chip local-demo">{LOCAL_DEMO_CHIP}</span>;
}

export interface DemoResultChipProps {
  kind: 'refusal-engine' | 'carry-engine';
}

export function DemoResultChip({ kind }: DemoResultChipProps): JSX.Element {
  const text = kind === 'refusal-engine' ? DEMO_RESULT_REFUSAL : DEMO_RESULT_CARRY;
  return (
    <span className="chip pass">
      {TickIcon}
      {text}
    </span>
  );
}
