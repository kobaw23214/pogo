// Historical authentication helpers are no longer executable product paths.
// Existing reports and offline judges remain available; do not adapt/replay them.
process.stderr.write('Retired authentication acceptance entrypoint: api_read/auth_status are removed. Use the current browser-use-v2 suite and a new output directory.\n');
process.exitCode = 2;
