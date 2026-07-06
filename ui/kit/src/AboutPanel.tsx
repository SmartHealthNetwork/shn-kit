// AboutPanel.tsx — renders the package-time versions.json manifest GET
// /api/about serves — the exact component versions
// (kit/shn-gateway/shn-sdk/brProvider/hapiImage/temurin) and IG sets the
// pipeline baked in, plus the build commit/timestamp. Owns its own fetch
// (mirrors FreeFormPanel's self-contained useEffect idiom) so StatusPanel
// just mounts it — no prop plumbing.
//
// A dev checkout has no packaged manifest (Config.ManifestPath == ""), which
// kitd answers as a 404-with-body — that reads here as "unavailable", never
// a fabricated/zeroed-out version table (shown-never-faked).
import { useEffect, useState } from 'react';
import type { JSX } from 'react';
import type { AboutManifest } from './types';
import { getAbout } from './api';

type AboutState =
  | { kind: 'loading' }
  | { kind: 'ready'; manifest: AboutManifest }
  | { kind: 'unavailable' };

export function AboutPanel(): JSX.Element {
  const [state, setState] = useState<AboutState>({ kind: 'loading' });

  useEffect(() => {
    let cancelled = false;
    getAbout()
      .then((manifest) => {
        if (!cancelled) setState({ kind: 'ready', manifest });
      })
      .catch(() => {
        if (!cancelled) setState({ kind: 'unavailable' });
      });
    return () => {
      cancelled = true;
    };
  }, []);

  if (state.kind === 'loading') {
    return (
      <div className="about-panel">
        <h2>About</h2>
      </div>
    );
  }

  if (state.kind === 'unavailable') {
    return (
      <div className="about-panel">
        <h2>About</h2>
        <p className="about-unavailable">development build — no packaged manifest</p>
      </div>
    );
  }

  const m = state.manifest;
  return (
    <div className="about-panel">
      <h2>About</h2>
      <dl className="about-facts">
        <div className="about-fact">
          <dt>Kit</dt>
          <dd>{m.kit}</dd>
        </div>
        <div className="about-fact">
          <dt>shn-gateway</dt>
          <dd>{m.modules['shn-gateway']}</dd>
        </div>
        <div className="about-fact">
          <dt>shn-sdk</dt>
          <dd>{m.modules['shn-sdk']}</dd>
        </div>
        <div className="about-fact">
          <dt>br-provider</dt>
          <dd>{m.brProvider}</dd>
        </div>
        <div className="about-fact">
          <dt>HAPI image</dt>
          <dd>{m.hapiImage}</dd>
        </div>
        <div className="about-fact">
          <dt>Temurin</dt>
          <dd>{m.temurin}</dd>
        </div>
        <div className="about-fact">
          <dt>Build</dt>
          <dd>
            {m.build.commit} ({m.build.timestamp})
          </dd>
        </div>
      </dl>
      <details className="about-igs">
        <summary>Validator IGs ({m.igsValidator.length})</summary>
        <ul>
          {m.igsValidator.map((ig) => (
            <li key={ig}>{ig}</li>
          ))}
        </ul>
      </details>
      <details className="about-igs">
        <summary>Data server IGs ({m.igsData.length})</summary>
        <ul>
          {m.igsData.map((ig) => (
            <li key={ig}>{ig}</li>
          ))}
        </ul>
      </details>
    </div>
  );
}

export default AboutPanel;
