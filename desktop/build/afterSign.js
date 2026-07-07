// desktop/build/afterSign.js — electron-builder's afterSign hook. mac signing
// (codesigning the .app) is handled by electron-builder from CSC_LINK/
// CSC_KEY_PASSWORD; the Windows installer is signed OUT-OF-BAND by the packaging
// workflow's Azure/artifact-signing-action step, not by electron-builder. When
// the mac secrets are unset, electron-builder produces an unsigned build and
// this hook still runs but every branch below no-ops. Notarization is mac-only and additionally needs
// an Apple ID app-specific password + team id; this hook checks for those
// explicitly and is the ONE conditional path that "overrides"
// electron-builder.yml's own `notarize: false` default when the secrets
// land (see that file's comment). A bare local `npm run pack` (no secrets)
// is always signing-ready but never blocked on signing — present, never
// required (signing is additive; the artifacts-only posture is unaffected
// either way).
exports.default = async function afterSign(context) {
  const { electronPlatformName } = context;
  if (electronPlatformName !== 'darwin') {
    return; // notarization is mac-only; win signing needs no post-sign hook
  }

  const { APPLE_ID, APPLE_APP_SPECIFIC_PASSWORD, APPLE_TEAM_ID } = process.env;
  if (!APPLE_ID || !APPLE_APP_SPECIFIC_PASSWORD || !APPLE_TEAM_ID) {
    console.log(
      'afterSign: APPLE_ID/APPLE_APP_SPECIFIC_PASSWORD/APPLE_TEAM_ID unset — skipping notarization (unsigned/local build).',
    );
    return;
  }

  // Required only on the conditional (signed + notarization-configured)
  // path — resolved from node_modules at call time so an unsigned/local
  // build never needs it present.
  const { notarize } = require('@electron/notarize');
  const path = require('node:path');

  const appName = context.packager.appInfo.productFilename;
  const appPath = path.join(context.appOutDir, `${appName}.app`);

  console.log(`afterSign: notarizing ${appPath} …`);
  await notarize({
    appPath,
    appleId: APPLE_ID,
    appleIdPassword: APPLE_APP_SPECIFIC_PASSWORD,
    teamId: APPLE_TEAM_ID,
  });
};
