import { gt as tr } from "@/i18n";
// Illustrative workflows only; never presented as live workspace activity.
export const landingScenarios = {
    get research() { return { role: tr('studioManagement.text515'), skill: tr('studioManagement.text516'), tools: tr('studioManagement.text517'), agent: 'research-agent', question: tr('studioManagement.text518'), answer: tr('studioManagement.text519'), steps: [tr('studioManagement.text520'), tr('studioManagement.text521'), tr('studioManagement.text522')], file: tr('studioManagement.text523'), title: [tr('studioManagement.text524'), tr('studioManagement.text525')], summary: [tr('studioManagement.text526'), tr('studioManagement.text527')], outline: [tr('studioManagement.text528'), tr('studioManagement.text529'), tr('studioManagement.text530')] }; },
    get engineering() { return { role: tr('studioManagement.text531'), skill: tr('studioManagement.text532'), tools: tr('studioManagement.text533'), agent: 'engineering-agent', question: tr('studioManagement.text534'), answer: tr('studioManagement.text535'), steps: [tr('studioManagement.text536'), tr('studioManagement.text537'), tr('studioManagement.text538')], file: tr('studioManagement.text539'), title: [tr('studioManagement.text540'), tr('studioManagement.text541')], summary: [tr('studioManagement.text542'), tr('studioManagement.text543')], outline: [tr('studioManagement.text544'), tr('studioManagement.text545'), tr('studioManagement.text546')] }; },
    get analysis() { return { role: tr('studioManagement.text547'), skill: tr('studioManagement.text548'), tools: tr('studioManagement.text549'), agent: 'analysis-agent', question: tr('studioManagement.text550'), answer: tr('studioManagement.text551'), steps: [tr('studioManagement.text552'), tr('studioManagement.text553'), tr('studioManagement.text554')], file: tr('studioManagement.text555'), title: [tr('studioManagement.text556'), tr('studioManagement.text557')], summary: [tr('studioManagement.text558'), tr('studioManagement.text559')], outline: [tr('studioManagement.text560'), tr('studioManagement.text561'), tr('studioManagement.text562')] }; }
  } as const;

export type LandingScenarioKey = keyof typeof landingScenarios;

export type CapabilityKey = 'agent' | 'model' | 'skill' | 'mcp';

// The left collage layers mirror the four capability folds: front card =
// agent, iris plate = models, back plate = skills, shell corner = mcp.
function anatomyLines(key: string): [string, string] {
  const [first = "", ...rest] = tr(`studioLandingLabels.${key}`).split("\n");
  return [first, rest.join(" ")];
}

export const anatomyFocusContent: Record<CapabilityKey, { caption: string; lines: [string, string]; tag: string }> = {
  get agent() { return { caption: tr("studioLandingLabels.agentCaption"), lines: anatomyLines("agentLines"), tag: tr("studioLandingLabels.agentTag") }; },
  get model() { return { caption: tr("studioLandingLabels.modelCaption"), lines: anatomyLines("modelLines"), tag: tr("studioLandingLabels.modelTag") }; },
  get skill() { return { caption: tr("studioLandingLabels.skillCaption"), lines: anatomyLines("skillLines"), tag: tr("studioLandingLabels.skillTag") }; },
  get mcp() { return { caption: tr("studioLandingLabels.mcpCaption"), lines: anatomyLines("mcpLines"), tag: tr("studioLandingLabels.mcpTag") }; },
};
