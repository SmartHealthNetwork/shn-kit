// RegisterSwitch.tsx — the Overview/Technical detail-level toggle that sits in
// the Scenarios column header. Pure presentational + dispatch: the caller
// (App, threaded through UCCards) owns `register` state.
//
// Deliberately a labeled GROUP of aria-pressed toggle buttons, NOT a
// tablist/tab. The lane ModeSwitch owns tablist semantics; this control just
// switches description density in place, so aria-pressed (toggle) is the
// correct role — and it must not reintroduce a tablist/tab inside the
// scenarios column (UCCards guards against exactly that).
import type { JSX } from 'react';
import type { Register } from './types';
import { REGISTERS, REGISTER_LABELS } from './ucmeta';

export interface RegisterSwitchProps {
  register: Register;
  onRegister(r: Register): void;
}

export function RegisterSwitch({ register, onRegister }: RegisterSwitchProps): JSX.Element {
  return (
    <div className="reg-switch" role="group" aria-label="Detail level">
      {REGISTERS.map((r) => (
        <button key={r} type="button" aria-pressed={register === r} onClick={() => onRegister(r)}>
          {REGISTER_LABELS[r]}
        </button>
      ))}
    </div>
  );
}
