// BootProgress.tsx — the Provision / Boot / Verify stages plus "Ready",
// rendered as a single staged list so the first-run user sees what each
// piece is as it comes up. Derivation mirrors waitKitReady: provisioned +
// gateway child 'ready' + the runs route live are the same three facts
// that gate both this screen and the kit-e2e/kitlive readiness gate.
import type { BootstrapResponse, StatusResponse } from './types';
import { canRestart, restartKit } from './bridge';

export interface BootProgressProps {
  boot: BootstrapResponse;
  status?: StatusResponse;
  runsLive: boolean;
}

type StageState = 'done' | 'active' | 'pending' | 'failed';

interface StageDef {
  key: string;
  label: string;
  subtitle: string;
  state: StageState;
  detail?: string;
}

export default function BootProgress({ boot, status, runsLive }: BootProgressProps) {
  const signInDone = boot.state !== 'signin-required' && boot.state !== 'signing-in';
  const provisionDone = boot.state === 'provisioned';

  const gatewayChild = status?.children.find((c) => c.name === 'gateway');
  const gatewayDone = gatewayChild?.state === 'ready';
  const gatewayFailed = gatewayChild?.state === 'failed';

  const verifyProbes = boot.verify;
  const verifyDone = verifyProbes.length === 3 && verifyProbes.every((p) => p.ok);
  const verifyFailed = verifyProbes.length > 0 && !verifyDone;
  const failingProbe = verifyProbes.find((p) => !p.ok);

  const readyDone = runsLive;

  // Same ordered facts waitKitReady polls, rendered as a staged list: each
  // stage is 'active' only once every stage before it is genuinely done (not
  // merely non-pending) — a failed predecessor blocks the ones after it.
  const doneFlags = [signInDone, provisionDone, gatewayDone, verifyDone, readyDone];
  const failedFlags = [false, false, gatewayFailed, verifyFailed, false];

  const clearBefore = (i: number) => doneFlags.slice(0, i).every(Boolean);

  const stageState = (i: number): StageState => {
    if (doneFlags[i]) return 'done';
    if (failedFlags[i]) return 'failed';
    if (clearBefore(i)) return 'active';
    return 'pending';
  };

  const stages: StageDef[] = [
    {
      key: 'signin',
      label: 'Sign in',
      subtitle: 'Authenticate through the developer portal in your system browser.',
      state: stageState(0),
    },
    {
      key: 'provision',
      label: 'Provision on the Hub',
      subtitle: 'Register this Kit as a provider participant on the preview Hub.',
      state: stageState(1),
    },
    {
      key: 'gateway',
      label: 'Start the Smart Gateway',
      subtitle: 'Launch the local Smart Gateway that exchanges with the Hub.',
      state: stageState(2),
      detail: gatewayFailed ? gatewayChild?.detail : undefined,
    },
    {
      key: 'verify',
      label: 'Verify the network',
      subtitle: 'Confirm registration, gateway federation, and payer reachability.',
      state: stageState(3),
      detail: verifyFailed && failingProbe ? `${failingProbe.name}: ${failingProbe.detail}` : undefined,
    },
    {
      key: 'ready',
      label: 'Ready',
      subtitle: 'The Kit is ready to run scenarios.',
      state: stageState(4),
    },
  ];

  return (
    <div className="phase-card boot-progress">
      <h1>Starting the Kit</h1>
      <ol className="stage-list">
        {stages.map((s) => (
          <li key={s.key} data-testid={`stage-${s.key}`} className={`stage stage-${s.state}`}>
            <div className="stage-header">
              <span className="stage-dot" aria-hidden="true" />
              <span className="stage-label">{s.label}</span>
            </div>
            <p className="stage-subtitle">{s.subtitle}</p>
            {s.detail && <p className="stage-detail">{s.detail}</p>}
          </li>
        ))}
      </ol>

      {gatewayFailed &&
        (canRestart() ? (
          <div className="stage-action">
            <button
              type="button"
              className="btn btn-primary"
              onClick={() => {
                void restartKit();
              }}
            >
              Restart the Kit
            </button>
          </div>
        ) : (
          <p className="stage-action">
            Restart shnkitd manually to retry starting the Smart Gateway.
          </p>
        ))}
    </div>
  );
}
