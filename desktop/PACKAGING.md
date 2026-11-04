# Local macOS development package

From the repository root, with the pinned Electron dependency already installed:

```sh
node pogo/desktop/package-app.mjs --dry-run
node pogo/desktop/package-app.mjs
```

The script produces `.localagent/checks/desktop-integration-20261008/artifacts/Pogo Desktop.app` for macOS arm64. It uses Electron's documented `Contents/Resources/app` layout, copies an explicit desktop/runtime source and production dependency set, and leaves an existing package as a timestamped backup when replacing it. It does not install to `/Applications`, alter quarantine attributes, register login items, or launch the GUI.

The package is **ad-hoc signed for local development**. `codesign --verify --deep --strict` and a packaged Electron/Node import check must pass before publication. It is not Developer ID signed or notarized; successful local verification is not evidence of Gatekeeper acceptance on other computers.

The app reads its private configuration from `~/Library/Application Support/Pogo Desktop/desktop.json` by default. The configuration uses `studioURL`, `workerPort`, an absolute `workerTokenFile` path, and optionally an absolute `studioTokenFile` path. Token files must be private (`0600`). Existing environment overrides remain supported by the main entry point. Create and manage that configuration outside the bundle; the package script never reads or copies it. The Studio service is external to this development package.

The adjacent `Pogo Desktop.package-manifest.json` records included payload paths and hashes, dependency versions, signature verification and import verification. Browser profiles, caches, repository tests and lab fixtures, credentials, the Go workspace, and the Electron npm installer dependency tree are excluded. The Electron framework and its native helper executables remain bundled.

References: [Electron manual packaging](https://www.electronjs.org/docs/latest/tutorial/application-distribution), [Electron code signing](https://www.electronjs.org/docs/latest/tutorial/code-signing).
