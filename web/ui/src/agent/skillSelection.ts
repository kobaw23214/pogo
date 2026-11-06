import { gt as tr } from "@/i18n";
import type { PlatformCapabilities } from './platformTypes';

export const isRuntimeSkillID = (id: string): boolean => /^rs_[0-9a-f]{64}$/.test(id);
export const skillStateLabel = (state?: string): string => ({
  available: tr('studioManagement.text647'), needs_configuration: tr('studioManagement.text648'), revoked: tr('studioManagement.text046'), unavailable: tr('studioManagement.text049'),
}[state ?? ''] ?? '');

/** Legacy knowledge skills have no lifecycle state; pending setup is not ready. */
export const skillIsReady = (skill: { state?: string }): boolean =>
  skill.state === undefined || skill.state === 'available';

export function skillChoices(catalog: PlatformCapabilities['skills'], selected: readonly string[]) {
  const available = catalog.filter(s => s.state !== 'revoked' && s.state !== 'unavailable');
  const availableIDs = new Set(available.map(s => s.id));
  const unavailable = selected.filter(id => !availableIDs.has(id)).map(id => {
    const skill = catalog.find(s => s.id === id);
    return { id, name: skill?.name ?? id, reason: skill?.state === 'revoked' ? tr('studioManagement.text046') : tr('studioManagement.text049') };
  });
  return { available, unavailable };
}
