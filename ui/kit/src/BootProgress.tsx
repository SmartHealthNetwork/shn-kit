// BootProgress.tsx — the Provision / Boot / Verify stages plus "Ready",
// rendered as a single staged list so the first-run user sees what each
// piece is as it comes up. Derivation mirrors waitKitReady: provisioned +
// gateway child 'ready' + the runs route live are the same three facts
// that gate both this screen and the kit-e2e/kitlive readiness gate.
//
// Boot attribution: the gateway is a Go binary that's ready in well
// under a second; the real ~2-3min first-boot wait is the bundled Java
// trio (validator/data-server/br-provider) building their databases. A
// single opaque "Start the Smart Gateway" stage hid that wait entirely —
// it now checks off fast, and the trio gets its own stage with per-server
// progress so the wait reads as "working", not "hung".
import type { BootstrapResponse, ChildStatus, StatusResponse } from './types';
import { canRestart, restartKit } from './bridge';

export interface BootProgressProps {
  boot: BootstrapResponse;
  status?: StatusResponse;
  runsLive: boolean;
}

type StageState = 'done' | 'active' | 'pending' | 'failed';

interface SubRow {
  name: string;
  label: string;
  state: 'done' | 'active' | 'waiting' | 'failed';
}

interface StageDef {
  key: string;
  label: string;
  subtitle: string;
  state: StageState;
  detail?: string;
  note?: string;
  subRows?: SubRow[];
}

// The bundled Java trio: kitd orders it AFTER the gateway child and it
// boots serially, one process at a time. Display labels are
// partner-facing copy; the keys are the wire names status.children
// carries (the packaged Java children).
const TRIO_ORDER = ['validator', 'data-server', 'br-provider'] as const;
const TRIO_LABELS: Record<(typeof TRIO_ORDER)[number], string> = {
  validator: 'Validator',
  'data-server': 'Data server',
  'br-provider': 'Provider system',
};
// Partner-facing label for a trio member by its wire name, falling back to
// the raw name for any child outside the canonical trio.
const trioLabel = (name: string): string =>
  TRIO_LABELS[name as (typeof TRIO_ORDER)[number]] ?? name;

// Always render all THREE canonical rows, looking each member up in
// status.children — NOT one row per present child. The trio boots
// serially and status.children only contains children the supervisor has
// already reached, so during real first-boot the later members are simply
// ABSENT (not present-and-'waiting'); iterating present children alone
// would show a list that grows 1→2→3 instead of the mockup's static
// 3-row picture. An absent member is treated as not-ready.
//
// phaseStarted gates the "in flight" paint: the trio boots strictly AFTER
// the gateway, so until the gateway is ready NONE of the trio has been
// launched and every member is honestly 'waiting' (nothing to spin). Once
// the phase has started, order-based (not per-state) derivation applies —
// a plain "ready → done, else → active" rule would mark every not-ready
// member active at once, which reads wrong the moment more than one hasn't
// reported ready. Since the trio boots strictly one at a time, among the
// not-ready-or-absent members only the first in canonical order is
// genuinely in flight — the rest are waiting their turn (an absent later
// member is honestly "waiting": it is queued in the serial boot, its
// process not yet started).
//
// A child that has terminally failed (the supervisor's failed/exited
// states, non-recoverable once its restart budget is spent) is painted
// FAILED — never a false "starting" spinner — regardless of phaseStarted,
// and is EXCLUDED from the "first non-ready = active" pick, so a dead
// server can't masquerade as the one in flight. A merely-restarting child
// is still trying, so it is NOT failed: it stays in the active/waiting
// derivation like any other not-ready member.
function isTrioTerminalFailure(state: string | undefined): boolean {
  return state === 'failed' || state === 'exited';
}
function deriveTrioSubRows(children: ChildStatus[], phaseStarted: boolean): SubRow[] {
  let activeAssigned = false;
  return TRIO_ORDER.map((name): SubRow => {
    const label = TRIO_LABELS[name];
    const child = children.find((c) => c.name === name);
    if (child?.state === 'ready') return { name, label, state: 'done' };
    if (isTrioTerminalFailure(child?.state)) return { name, label, state: 'failed' };
    if (phaseStarted && !activeAssigned) {
      activeAssigned = true;
      return { name, label, state: 'active' };
    }
    return { name, label, state: 'waiting' };
  });
}

export default function BootProgress({ boot, status, runsLive }: BootProgressProps) {
  const signInDone = boot.state !== 'signin-required' && boot.state !== 'signing-in';
  const provisionDone = boot.state === 'provisioned';

  const gatewayChild = status?.children.find((c) => c.name === 'gateway');
  const gatewayDone = gatewayChild?.state === 'ready';
  const gatewayFailed = gatewayChild?.state === 'failed';

  const allChildren = status?.children ?? [];
  // Whether the Kit BUNDLES the Java trio at all. The honest, immediately-
  // available signal is the daemon's own posture: SetStackInfo publishes
  // validator:'packaged' the moment BuildStack resolves — BEFORE any child
  // is started — so the stage renders from the first status poll instead of
  // popping in only once a trio child appears (which, with the gateway now
  // ordered first, happens only after the gateway finishes). A dev build
  // reports 'stand-in' (or omits the field), so it never grows this stage.
  // The "a trio child is already present" fallback keeps the stage for an
  // old daemon that predates the validator field but is running the trio.
  const trioChildPresent = TRIO_ORDER.some((name) => allChildren.some((c) => c.name === name));
  const trioExpected = status?.validator === 'packaged' || trioChildPresent;
  // Done only when EVERY canonical member is present AND ready — an absent
  // later member means the serial boot hasn't reached it yet, so the trio
  // is not done (and must not unblock verify/ready).
  const trioAllReady = TRIO_ORDER.every((name) =>
    allChildren.some((c) => c.name === name && c.state === 'ready'),
  );
  // A present trio member that has terminally failed (restart budget spent)
  // wedges the boot forever with no recovery unless we surface it: without
  // this, trioAllReady stays false, the stage stays 'active', and verify +
  // ready hang 'pending' painting a DEAD server as "starting". Only counts
  // when the trio is actually present — a dev build without the packaged
  // Java assets has no trio to fail (fhirGateDone below stays transparently
  // done for it).
  const failedTrioChildren = TRIO_ORDER.map((name) =>
    allChildren.find((c) => c.name === name),
  ).filter((c): c is ChildStatus => !!c && isTrioTerminalFailure(c.state));
  const trioFailed = failedTrioChildren.length > 0;

  const verifyProbes = boot.verify;
  const verifyDone = verifyProbes.length === 3 && verifyProbes.every((p) => p.ok);
  const verifyFailed = verifyProbes.length > 0 && !verifyDone;
  const failingProbe = verifyProbes.find((p) => !p.ok);

  const readyDone = runsLive;

  // Ordered-clear chain — same ordered facts waitKitReady polls, rendered
  // as a staged list: each stage is 'active' only once EVERY prior stage is
  // genuinely done (not merely non-pending), and a failed predecessor
  // blocks the ones after it. The boot is now genuinely sequential
  // (gateway → validator → data-server → br-provider), so the FHIR-servers
  // stage rides the chain at index 3 like every other stage — 'pending'
  // (greyed, upcoming) until the fast gateway is done, THEN 'active'. A
  // not-present trio is transparently "done" (fhirGateDone) so a dev build
  // never wedges verify/ready pending.
  const fhirGateDone = !trioExpected || trioAllReady;
  const doneFlags = [signInDone, provisionDone, gatewayDone, fhirGateDone, verifyDone, readyDone];
  // Index 3 is the fhirservers/fhirGateDone slot: a terminally-failed trio
  // child makes stageState(3) return 'failed' instead of the stage (and the
  // chain after it) wedging 'active'/'pending' forever.
  const failedFlags = [false, false, gatewayFailed, trioFailed, verifyFailed, false];

  const clearBefore = (i: number) => doneFlags.slice(0, i).every(Boolean);

  const stageState = (i: number): StageState => {
    if (doneFlags[i]) return 'done';
    if (failedFlags[i]) return 'failed';
    if (clearBefore(i)) return 'active';
    return 'pending';
  };

  // The FHIR-servers stage IS an ordinary link in the ordered chain now
  // that the trio boots AFTER the gateway. (It once carried a bespoke state
  // because the trio used to boot BEFORE the gateway, when gating it behind
  // gatewayDone would have hidden real progress — no longer true, and the
  // reason it appeared to "pop in" only after the gateway finished.) Its
  // sub-rows paint an "in flight" member only once the gateway is done —
  // i.e. once the serial trio phase has actually begun; before that every
  // member reads 'waiting'.
  const fhirStageState = stageState(3);
  const trioSubRows = deriveTrioSubRows(allChildren, gatewayDone);

  // Name the failed FHIR server(s) on the stage's detail line, mirroring the
  // gateway-failed path that surfaces gatewayChild.detail — so the user sees
  // WHICH server died and why, not just a red row.
  const trioFailedDetail = trioFailed
    ? failedTrioChildren
        .map((c) => (c.detail ? `${trioLabel(c.name)}: ${c.detail}` : trioLabel(c.name)))
        .join('; ')
    : undefined;

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
      subtitle: 'Ready in under a second.',
      state: stageState(2),
      detail: gatewayFailed ? gatewayChild?.detail : undefined,
    },
    // Present only when the trio is expected — the daemon reports the
    // packaged validator posture (available before any child starts, so no
    // pop-in), or a trio child is already present. A dev build with the
    // stand-in validator never grows this stage.
    ...(trioExpected
      ? [
          {
            key: 'fhirservers',
            label: 'Starting the FHIR servers',
            subtitle: 'First launch builds their databases — this can take a couple of minutes.',
            state: fhirStageState,
            detail: trioFailedDetail,
            note: 'You only wait this long the first time — later launches reuse the prepared databases.',
            subRows: trioSubRows,
          } satisfies StageDef,
        ]
      : []),
    {
      key: 'verify',
      label: 'Verify the network',
      subtitle: 'Confirm registration, gateway federation, and payer reachability.',
      state: stageState(4),
      detail: verifyFailed && failingProbe ? `${failingProbe.name}: ${failingProbe.detail}` : undefined,
    },
    {
      key: 'ready',
      label: 'Ready',
      subtitle: 'The Kit is ready to run scenarios.',
      state: stageState(5),
    },
  ];

  // While any stage is still active, the boot isn't done — but it also
  // hasn't failed, so this is exactly the "quiet middle" where a slow
  // stage (the trio, typically) can read as hung. Once every stage is done
  // (Ready) or one has failed, no stage is 'active' and the hint naturally
  // disappears.
  //
  // But while the FHIR-servers stage is the active one it already carries
  // its OWN "first launch builds their databases…" subtitle + "you only
  // wait this long the first time" note — the generic boot-hint would then
  // be a second, overlapping "why is this slow" message. The mockup shows
  // one, so suppress the generic hint exactly then; other in-progress
  // stages (which have no such note) still get it.
  const fhirServersActive = trioExpected && fhirStageState === 'active';
  const bootInProgress = stages.some((s) => s.state === 'active');
  const showBootHint = bootInProgress && !fhirServersActive;

  // The restart affordance covers BOTH terminal-boot failures — a dead
  // gateway child OR a dead FHIR server — since either leaves the boot wedged
  // with no recovery. restartKit() bounces the whole daemon, which restarts
  // the gateway and the trio together, so it is the correct recovery for
  // both. Gateway failure is the more fundamental one, so its copy wins when
  // (rarely) both are failed at once.
  const bootFailed = gatewayFailed || trioFailed;
  const restartTarget = gatewayFailed ? 'the Smart Gateway' : 'the FHIR servers';

  return (
    <div className="phase-card boot-progress">
      <h1>Starting the Kit</h1>
      <ol className="stage-list">
        {stages.map((s) => (
          <li key={s.key} data-testid={`stage-${s.key}`} className={`stage stage-${s.state}`}>
            <div className="stage-header">
              <span className="stage-icon" aria-hidden="true">
                {s.state === 'active' ? (
                  <span className="stage-spinner" data-testid="stage-spinner" />
                ) : (
                  <span className="stage-dot" />
                )}
              </span>
              <span className="stage-label">{s.label}</span>
            </div>
            <p className="stage-subtitle">{s.subtitle}</p>
            {s.detail && <p className="stage-detail">{s.detail}</p>}
            {s.subRows && (
              <ul className="stage-substeps">
                {s.subRows.map((row) => (
                  <li
                    key={row.name}
                    data-testid={`stage-sub-${row.name}`}
                    className={`stage-sub stage-sub-${row.state}`}
                  >
                    <span className="stage-sub-icon" aria-hidden="true">
                      {row.state === 'active' ? (
                        <span className="stage-sub-spinner" />
                      ) : (
                        <span className="stage-sub-dot" />
                      )}
                    </span>
                    <span className="stage-sub-label">{row.label}</span>
                    <span className="stage-sub-status">
                      {row.state === 'done'
                        ? 'ready'
                        : row.state === 'waiting'
                          ? 'waiting'
                          : row.state === 'failed'
                            ? 'failed'
                            : 'starting'}
                    </span>
                  </li>
                ))}
              </ul>
            )}
            {s.note && <p className="stage-note">{s.note}</p>}
          </li>
        ))}
      </ol>

      {showBootHint && (
        <p className="boot-hint" data-testid="boot-hint">
          First launch starts the local servers — this can take a couple of minutes.
        </p>
      )}

      {bootFailed &&
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
            Restart shnkitd manually to retry starting {restartTarget}.
          </p>
        ))}
    </div>
  );
}
