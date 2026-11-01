# Retired tabCapture source

These files and their original hashes preserve the previously accepted W1
implementation for investigation and rollback provenance. They are not imported
by the production worker, included in its Docker image, or selected as a runtime
fallback. The active worker uses `src/wayland.js` and `wayland/` exclusively.
Historical extension-specific tests are under `test/`; the normal Node test suite
is `../../test/*.test.js`. Product recovery restarts the same Wayland runtime.

W1 accepted Docker image and release evidence remain available independently;
reverting a complete release is a deliberate transaction, not a second live path.
