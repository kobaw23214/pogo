import { retiredAuthProfile } from '../retired-profile.js';
import { TASKS } from './tasks.js';

if (process.argv.includes('--list')) {
  process.stdout.write(JSON.stringify(TASKS, null, 2) + '\n');
} else if (process.argv.includes('--help')) {
  process.stdout.write(retiredAuthProfile('auth-auto').message + '\nOnly --list (historical task metadata) and offline report analysis remain available.\n');
} else {
  process.stderr.write(retiredAuthProfile('auth-auto').message + '\n');
  process.exitCode = 2;
}
