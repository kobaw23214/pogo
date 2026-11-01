export interface PackageRef {
  id: string;
  version: string;
  manifestDigest: string;
  packageDigest: string;
}

export interface SkillExecutionStatus {
  configured: boolean;
  host_platform: string;
  expected_platform?: string;
  supported_platforms: string[];
  backend?: string;
  label?: string;
  resource_limits?: string;
  issue_code?: string;
  issue?: string;
}

export interface SkillExecutionIssue {
  code: string;
  message: string;
}
