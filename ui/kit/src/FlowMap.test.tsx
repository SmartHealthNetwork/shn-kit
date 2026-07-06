import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { FlowMap, REMOTE_ZONE_CAPTION, EHR_PROVIDER_LABEL, CONFORMANT_PROVIDER_LABEL } from './FlowMap';
import { buildRunStory } from './inspect';
import type { Step, RunStory } from './inspect';
import type { KitEvent } from './types';
import ehrUc03 from './fixtures/run-ehr-uc03.json';

const ehrEvents = ehrUc03 as unknown as KitEvent[];
const ehrStory = buildRunStory(ehrEvents[0].runId as string, ehrEvents);

function emptyStory(): RunStory {
  return { runId: 'run-empty', steps: [], audit: [] };
}

function ingressStep(): Step {
  return {
    id: '1',
    kind: 'ingress',
    legType: 'crd-ingress',
    status: 'ok',
    request: { seq: 1, time: '2026-07-03T00:00:00Z', kind: 'ingress.received', legType: 'crd-ingress' },
    response: { seq: 2, time: '2026-07-03T00:00:01Z', kind: 'ingress.responded', legType: 'crd-ingress', detail: '200' },
    httpStatus: '200',
    narration: 'ingress narration',
  };
}

function openLegStep(): Step {
  return {
    id: '2',
    kind: 'leg',
    legType: 'pas-claim',
    status: 'open',
    request: {
      seq: 2,
      time: '2026-07-03T00:00:00Z',
      kind: 'leg.originated',
      legType: 'pas-claim',
      correlationId: 'c-1',
      counterpart: 'payer',
    },
    correlationId: 'c-1',
    counterpart: 'payer',
    narration: 'open leg narration',
  };
}

function okLegStep(counterpart = 'payer'): Step {
  return {
    id: '3',
    kind: 'leg',
    legType: 'pas-claim',
    status: 'ok',
    request: {
      seq: 3,
      time: '2026-07-03T00:00:00Z',
      kind: 'leg.originated',
      legType: 'pas-claim',
      correlationId: 'c-2',
      counterpart,
    },
    response: {
      seq: 4,
      time: '2026-07-03T00:00:01Z',
      kind: 'leg.response',
      legType: 'pas-claim',
      correlationId: 'c-2',
    },
    correlationId: 'c-2',
    counterpart,
    narration: 'ok leg narration',
  };
}

function failedLegStep(): Step {
  return {
    id: '4',
    kind: 'leg',
    legType: 'pas-claim',
    status: 'failed',
    request: {
      seq: 5,
      time: '2026-07-03T00:00:00Z',
      kind: 'leg.originated',
      legType: 'pas-claim',
      correlationId: 'c-3',
      counterpart: 'payer',
    },
    response: {
      seq: 6,
      time: '2026-07-03T00:00:01Z',
      kind: 'leg.failed',
      legType: 'pas-claim',
      correlationId: 'c-3',
      detail: 'timed out',
    },
    correlationId: 'c-3',
    counterpart: 'payer',
    narration: 'failed leg narration',
  };
}

function validateStep(): Step {
  return {
    id: '5',
    kind: 'validate',
    legType: 'validate.result',
    status: 'ok',
    request: { seq: 7, time: '2026-07-03T00:00:00Z', kind: 'validate.result', detail: 'valid' },
    validation: 'valid',
    narration: 'validate narration',
  };
}

const NODE_IDS = ['provider', 'gateway', 'validator', 'hub', 'payer-gateway', 'payer-engine'];

function getNode(id: string): HTMLElement {
  const el = document.querySelector(`[data-node="${id}"]`);
  if (!el) throw new Error(`missing data-node=${id}`);
  return el as HTMLElement;
}

describe('FlowMap — node rail + remote zone', () => {
  it('renders all six nodes and the remote-zone container with the pinned caption', () => {
    render(<FlowMap story={emptyStory()} lane="conformant" onSelectStep={() => {}} />);

    for (const id of NODE_IDS) {
      expect(getNode(id)).toBeDefined();
    }
    const remoteZone = document.querySelector('.remote-zone');
    expect(remoteZone).not.toBeNull();
    for (const id of ['hub', 'payer-gateway', 'payer-engine']) {
      expect(remoteZone?.contains(getNode(id))).toBe(true);
    }
    expect(screen.getByText(REMOTE_ZONE_CAPTION)).toBeDefined();
    expect(REMOTE_ZONE_CAPTION).toBe(
      'derived from what the Smart Gateway sent and the verified response it received — the Kit does not observe inside the hosted side',
    );
  });

  it('ehr lane: provider node is labeled "Plain EHR (seeded data source)", marked data-static="true", and never lit even with ingress steps', () => {
    const story: RunStory = { runId: 'run-1', steps: [ingressStep()], audit: [] };
    render(<FlowMap story={story} lane="ehr" onSelectStep={() => {}} />);

    const provider = getNode('provider');
    expect(provider.textContent).toContain(EHR_PROVIDER_LABEL);
    expect(provider.getAttribute('data-static')).toBe('true');
    expect(provider.className).not.toContain('lit');
  });

  it('conformant lane: provider node is labeled "Provider system" and lights when the story has ingress steps', () => {
    const withIngress: RunStory = { runId: 'run-2', steps: [ingressStep()], audit: [] };
    render(<FlowMap story={withIngress} lane="conformant" onSelectStep={() => {}} />);

    const provider = getNode('provider');
    expect(provider.textContent).toContain(CONFORMANT_PROVIDER_LABEL);
    expect(provider.className).toContain('lit');
    expect(provider.getAttribute('data-static')).not.toBe('true');
  });

  it('conformant lane: provider node is NOT lit when the story has no ingress steps', () => {
    const noIngress: RunStory = { runId: 'run-3', steps: [okLegStep()], audit: [] };
    render(<FlowMap story={noIngress} lane="conformant" onSelectStep={() => {}} />);

    const provider = getNode('provider');
    expect(provider.className).not.toContain('lit');
  });
});

describe('FlowMap — steps render in order, selection, click', () => {
  it('renders steps in seq order as buttons with correct data-from/to/status, and clicking calls onSelectStep', async () => {
    const user = userEvent.setup();
    const onSelectStep = vi.fn();
    const story: RunStory = { runId: 'run-4', steps: [ingressStep(), validateStep(), okLegStep()], audit: [] };
    render(<FlowMap story={story} lane="conformant" selectedStepId={undefined} onSelectStep={onSelectStep} />);

    const buttons = Array.from(document.querySelectorAll('.flow-step')) as HTMLElement[];
    expect(buttons).toHaveLength(3);
    expect(buttons.map((b) => b.getAttribute('data-step-id'))).toEqual(['1', '5', '3']);

    expect(buttons[0].getAttribute('data-from')).toBe('provider');
    expect(buttons[0].getAttribute('data-to')).toBe('gateway');
    expect(buttons[0].getAttribute('data-status')).toBe('ok');

    expect(buttons[1].getAttribute('data-from')).toBe('gateway');
    expect(buttons[1].getAttribute('data-to')).toBe('validator');
    expect(buttons[1].getAttribute('data-status')).toBe('ok');

    expect(buttons[2].getAttribute('data-from')).toBe('gateway');
    expect(buttons[2].getAttribute('data-to')).toBe('remote');
    expect(buttons[2].getAttribute('data-status')).toBe('ok');

    await user.click(buttons[2]);
    expect(onSelectStep).toHaveBeenCalledWith('3');
  });

  it('marks the selected step with class "selected"', () => {
    const story: RunStory = { runId: 'run-5', steps: [okLegStep()], audit: [] };
    render(<FlowMap story={story} lane="conformant" selectedStepId="3" onSelectStep={() => {}} />);

    const button = document.querySelector('.flow-step') as HTMLElement;
    expect(button.className).toContain('selected');
  });

  it('does not mark an unselected step as selected', () => {
    const story: RunStory = { runId: 'run-6', steps: [okLegStep()], audit: [] };
    render(<FlowMap story={story} lane="conformant" selectedStepId="not-this-one" onSelectStep={() => {}} />);

    const button = document.querySelector('.flow-step') as HTMLElement;
    expect(button.className).not.toContain('selected');
  });

  it('sets aria-pressed to reflect selection state independently per button', () => {
    const story: RunStory = {
      runId: 'run-6b',
      steps: [okLegStep(), { ...failedLegStep(), id: '4b' }],
      audit: [],
    };
    render(<FlowMap story={story} lane="conformant" selectedStepId="3" onSelectStep={() => {}} />);

    const buttons = Array.from(document.querySelectorAll('.flow-step')) as HTMLElement[];
    expect(buttons[0].getAttribute('aria-pressed')).toBe('true');
    expect(buttons[1].getAttribute('aria-pressed')).toBe('false');
  });
});

describe('FlowMap — remote-zone honesty (shown-never-faked)', () => {
  it('a leg step with a response lights the remote-zone nodes ("lit remote")', () => {
    const story: RunStory = { runId: 'run-7', steps: [okLegStep()], audit: [] };
    render(<FlowMap story={story} lane="conformant" onSelectStep={() => {}} />);

    for (const id of ['hub', 'payer-gateway', 'payer-engine']) {
      const node = getNode(id);
      expect(node.className).toContain('lit');
      expect(node.className).toContain('remote');
    }
  });

  it('a story whose only leg step is open (no response yet) does NOT light the remote interior', () => {
    const story: RunStory = { runId: 'run-8', steps: [openLegStep()], audit: [] };
    render(<FlowMap story={story} lane="conformant" onSelectStep={() => {}} />);

    for (const id of ['hub', 'payer-gateway', 'payer-engine']) {
      const node = getNode(id);
      expect(node.className).not.toContain('lit');
    }
  });

  it('a leg.failed step marks its button data-status="failed" and marks the remote zone edge failed', () => {
    const story: RunStory = { runId: 'run-9', steps: [failedLegStep()], audit: [] };
    render(<FlowMap story={story} lane="conformant" onSelectStep={() => {}} />);

    const button = document.querySelector('.flow-step') as HTMLElement;
    expect(button.getAttribute('data-status')).toBe('failed');

    const remoteZone = document.querySelector('.remote-zone') as HTMLElement;
    expect(remoteZone.className).toContain('failed');
    // failure alone must not fake a lit (confirmed) response
    for (const id of ['hub', 'payer-gateway', 'payer-engine']) {
      expect(getNode(id).className).not.toContain('lit');
    }
  });

  it('mixed matrix cell: one ok leg AND one separate failed leg — remote-zone carries BOTH flags, interior is honestly lit (a genuine response occurred), and each step keeps its own independent data-status', () => {
    const story: RunStory = { runId: 'run-11', steps: [okLegStep(), failedLegStep()], audit: [] };
    render(<FlowMap story={story} lane="conformant" onSelectStep={() => {}} />);

    const remoteZone = document.querySelector('.remote-zone') as HTMLElement;
    expect(remoteZone.className).toContain('lit');
    expect(remoteZone.className).toContain('failed');

    for (const id of ['hub', 'payer-gateway', 'payer-engine']) {
      expect(getNode(id).className).toContain('lit');
    }

    const buttons = Array.from(document.querySelectorAll('.flow-step')) as HTMLElement[];
    expect(buttons).toHaveLength(2);
    expect(buttons[0].getAttribute('data-step-id')).toBe('3');
    expect(buttons[0].getAttribute('data-status')).toBe('ok');
    expect(buttons[1].getAttribute('data-step-id')).toBe('4');
    expect(buttons[1].getAttribute('data-status')).toBe('failed');
  });

  it('zero-leg matrix cell: a validate-only story lights no remote node and leaves the remote-zone neither lit nor failed', () => {
    const story: RunStory = { runId: 'run-12', steps: [validateStep()], audit: [] };
    render(<FlowMap story={story} lane="conformant" onSelectStep={() => {}} />);

    for (const id of ['hub', 'payer-gateway', 'payer-engine']) {
      expect(getNode(id).className).not.toContain('lit');
    }

    const remoteZone = document.querySelector('.remote-zone') as HTMLElement;
    expect(remoteZone.className).not.toContain('lit');
    expect(remoteZone.className).not.toContain('failed');
  });
});

function okFederatedQueryStep(): Step {
  return {
    id: '6',
    kind: 'leg',
    legType: 'federated-query-submit',
    status: 'ok',
    request: {
      seq: 8,
      time: '2026-07-03T00:00:00Z',
      kind: 'leg.originated',
      legType: 'federated-query-submit',
      correlationId: 'c-4',
      counterpart: 'facility-a',
    },
    response: {
      seq: 9,
      time: '2026-07-03T00:00:01Z',
      kind: 'leg.response',
      legType: 'federated-query-submit',
      correlationId: 'c-4',
    },
    correlationId: 'c-4',
    counterpart: 'facility-a',
    narration: 'federated query narration',
  };
}

function okUnknownLegTypeStep(): Step {
  return {
    id: '7',
    kind: 'leg',
    legType: 'mystery-leg-type',
    status: 'ok',
    request: {
      seq: 10,
      time: '2026-07-03T00:00:00Z',
      kind: 'leg.originated',
      legType: 'mystery-leg-type',
      correlationId: 'c-5',
      counterpart: 'some-unknown-counterpart',
    },
    response: {
      seq: 11,
      time: '2026-07-03T00:00:01Z',
      kind: 'leg.response',
      legType: 'mystery-leg-type',
      correlationId: 'c-5',
    },
    correlationId: 'c-5',
    counterpart: 'some-unknown-counterpart',
    narration: 'unrecognized legType narration',
  };
}

describe('FlowMap — providerLabel override', () => {
  it('ehr lane: providerLabel overrides the node text but keeps data-static="true" and never lit', () => {
    const story: RunStory = { runId: 'run-p1', steps: [ingressStep()], audit: [] };
    render(
      <FlowMap
        story={story}
        lane="ehr"
        providerLabel="Your EHR (FHIR data source)"
        onSelectStep={() => {}}
      />,
    );

    const provider = getNode('provider');
    expect(provider.textContent).toContain('Your EHR (FHIR data source)');
    expect(provider.getAttribute('data-static')).toBe('true');
    expect(provider.className).not.toContain('lit');
  });

  it('conformant lane: providerLabel overrides the node text and still lights on ingress', () => {
    const story: RunStory = { runId: 'run-p2', steps: [ingressStep()], audit: [] };
    render(
      <FlowMap
        story={story}
        lane="conformant"
        providerLabel="Your Da Vinci system"
        onSelectStep={() => {}}
      />,
    );

    const provider = getNode('provider');
    expect(provider.textContent).toContain('Your Da Vinci system');
    expect(provider.className).toContain('lit');
    expect(provider.getAttribute('data-static')).not.toBe('true');
  });
});

describe('FlowMap — payer-node gating by legType family', () => {
  it("a story whose only ok leg's counterpart is the payer (a payer-family legType) lights hub + payer-gateway + payer-engine", () => {
    const story: RunStory = { runId: 'run-m1-1', steps: [okLegStep('payer')], audit: [] };
    render(<FlowMap story={story} lane="conformant" onSelectStep={() => {}} />);

    for (const id of ['hub', 'payer-gateway', 'payer-engine']) {
      expect(getNode(id).className).toContain('lit');
    }
  });

  it('a story whose only ok leg is a NON-payer counterpart (federated-query-submit) lights the Hub only — the rendered-story lie this guards against, now pinned', () => {
    const story: RunStory = { runId: 'run-m1-2', steps: [okFederatedQueryStep()], audit: [] };
    render(<FlowMap story={story} lane="conformant" onSelectStep={() => {}} />);

    expect(getNode('hub').className).toContain('lit');
    expect(getNode('payer-gateway').className).not.toContain('lit');
    expect(getNode('payer-engine').className).not.toContain('lit');
  });

  it('a mixed story (one payer ok leg + one non-payer ok leg) lights all three remote nodes', () => {
    const story: RunStory = {
      runId: 'run-m1-3',
      steps: [okLegStep('payer'), okFederatedQueryStep()],
      audit: [],
    };
    render(<FlowMap story={story} lane="conformant" onSelectStep={() => {}} />);

    for (const id of ['hub', 'payer-gateway', 'payer-engine']) {
      expect(getNode(id).className).toContain('lit');
    }
  });

  it('zero ok legs (open + failed only) leaves the remote interior unlit — existing row stays', () => {
    const story: RunStory = {
      runId: 'run-m1-4',
      steps: [openLegStep(), failedLegStep()],
      audit: [],
    };
    render(<FlowMap story={story} lane="conformant" onSelectStep={() => {}} />);

    for (const id of ['hub', 'payer-gateway', 'payer-engine']) {
      expect(getNode(id).className).not.toContain('lit');
    }
  });

  it('an ok leg with an UNRECOGNIZED legType lights the Hub only — the honest degradation fallback (isPayerLegType\'s default false, previously unpinned)', () => {
    const story: RunStory = { runId: 'run-m1-5', steps: [okUnknownLegTypeStep()], audit: [] };
    render(<FlowMap story={story} lane="conformant" onSelectStep={() => {}} />);

    expect(getNode('hub').className).toContain('lit');
    expect(getNode('payer-gateway').className).not.toContain('lit');
    expect(getNode('payer-engine').className).not.toContain('lit');
  });
});

describe('FlowMap — per-step counterpart labeling', () => {
  it("a leg step's rendered label shows its OWN counterpart holder id, not a hardcoded payer", () => {
    const story: RunStory = {
      runId: 'run-10',
      steps: [okLegStep('payer'), okLegStep('facility-a')],
      audit: [],
    };
    // give the second step a distinct id so both buttons render distinctly
    story.steps[1] = { ...story.steps[1], id: '3b', counterpart: 'facility-a' };

    render(<FlowMap story={story} lane="conformant" onSelectStep={() => {}} />);

    expect(screen.getByText('payer')).toBeDefined();
    expect(screen.getByText('facility-a')).toBeDefined();
  });
});

describe('FlowMap — fixture replay (run-ehr-uc03.json)', () => {
  it('renders one step button per story.steps.length; gateway, validator, and remote zone all lit', () => {
    render(<FlowMap story={ehrStory} lane="ehr" onSelectStep={() => {}} />);

    const buttons = document.querySelectorAll('.flow-step');
    expect(buttons).toHaveLength(ehrStory.steps.length);

    expect(getNode('gateway').className).toContain('lit');
    expect(getNode('validator').className).toContain('lit');
    for (const id of ['hub', 'payer-gateway', 'payer-engine']) {
      expect(getNode(id).className).toContain('lit');
    }
  });
});
