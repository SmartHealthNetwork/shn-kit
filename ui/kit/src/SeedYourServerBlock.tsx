// SeedYourServerBlock.tsx — the "Seed your server" download block mounted
// inside each BYOPanel lane section (BYOPanel.tsx): a partner downloads the
// lane's synthetic seed bundle and POSTs it to their own FHIR server
// themselves — the Kit never writes to a partner's connected server itself.
// The download reuses StatusPanel.tsx's handleDownloadBundle
// shape exactly: resolveToken() -> fetch(..., {Authorization: Bearer}) ->
// blob -> object URL -> a programmatically-created `<a download>` click ->
// revoke in finally, with a local error state on failure (never an
// unhandled rejection).
import { useState } from 'react';
import type { JSX } from 'react';
import { resolveToken } from './bridge';
import { seedBundleUrl } from './api';

export interface SeedYourServerBlockProps {
  lane: 'ehr' | 'conformant';
  // The FHIR base URL to interpolate into the POST recipe: the EHR
  // section's own typed dataUrl (falling back to a placeholder while
  // empty), or a bare placeholder for the Da Vinci section (inbound-only —
  // there is no typed data URL to echo there).
  postBase: string;
}

export function SeedYourServerBlock({ lane, postBase }: SeedYourServerBlockProps): JSX.Element {
  const [error, setError] = useState<string | undefined>(undefined);

  const filename = `shn-${lane}-personas.json`;
  const recipe = `curl -X POST -H "Content-Type: application/fhir+json" --data-binary @${filename} ${postBase}`;

  const handleDownload = async () => {
    setError(undefined);
    try {
      const token = await resolveToken();
      const res = await fetch(seedBundleUrl(lane), {
        headers: { Authorization: `Bearer ${token}` },
      });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const blob = await res.blob();
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = filename;
      try {
        a.click();
      } finally {
        URL.revokeObjectURL(url);
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  };

  return (
    <div className="byo-seed-block">
      <h4>Seed your server</h4>
      <button
        type="button"
        className="btn ghost"
        onClick={() => {
          void handleDownload();
        }}
      >
        Download seed bundle
      </button>
      <pre className="byo-seed-recipe">
        <code>{recipe}</code>
      </pre>
      <p className="byo-seed-note">
        the demo payer only covers the member ids seeded in this bundle — an id your server carries beyond that
        won&apos;t be runnable
      </p>
      {error && (
        <p role="alert" className="byo-seed-error">
          {error}
        </p>
      )}
    </div>
  );
}

export default SeedYourServerBlock;
