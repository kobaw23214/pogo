package skillruntime

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/yosukeno/pogo/skillruntime/core"
	"sort"
	"strings"
)

// Selection contains user intent only. Providers, resources and grants come
// from the selected Skill's verified declaration, never a separate tool picker.
type Selection struct {
	SkillIDs []string `json:"skill_ids"`
}

func (s *Selection) Empty() bool { return s == nil || len(s.SkillIDs) == 0 }

type SelectionIssue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type ToolEntry struct {
	ID            string              `json:"id"`
	Package       core.PackageRef     `json:"package"`
	PackageName   string              `json:"package_name"`
	ProviderID    string              `json:"provider_id"`
	Contract      core.ContractRef    `json:"contract"`
	Description   string              `json:"description"`
	Revoked       bool                `json:"revoked"`
	ResourceSlots []core.ResourceSlot `json:"resource_slots"`
	AssetIDs      []string            `json:"asset_ids,omitempty"`
}
type SkillEntry struct {
	ID               string             `json:"id"`
	Name             string             `json:"name"`
	Description      string             `json:"description"`
	Source           string             `json:"source"`
	Package          core.PackageRef    `json:"package"`
	PackageName      string             `json:"package_name"`
	SkillID          string             `json:"skill_id"`
	ToolCount        int                `json:"tool_count"`
	Required         []core.ContractRef `json:"required_capabilities"`
	State            string             `json:"state"`
	Issues           []SelectionIssue   `json:"issues"`
	RuntimeCount     int                `json:"runtime_count"`
	AssetCount       int                `json:"asset_count"`
	PackageSizeBytes int64              `json:"package_size_bytes"`
	InstalledAt      string             `json:"installed_at,omitempty"`
	Removable        bool               `json:"removable"`
}
type SelectionResource struct {
	PackageID   string `json:"packageId"`
	SlotID      string `json:"slotId"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
	ResourceRef string `json:"resourceRef"`
}
type SelectionPlan struct {
	Execution ExecutionStatus     `json:"execution"`
	Selection Selection           `json:"selection"`
	Ready     bool                `json:"ready"`
	Issues    []SelectionIssue    `json:"issues"`
	Tools     []ToolEntry         `json:"tools"`
	Resources []SelectionResource `json:"resources"`
	Binding   json.RawMessage     `json:"-"`
}
type SelectionCatalog struct {
	Skills              []SkillEntry `json:"skills"`
	Tools               []ToolEntry  `json:"tools"`
	ExecutionConfigured bool         `json:"execution_configured"`
}

func PackageSkillID(ref core.PackageRef) string {
	return "rs_" + strings.TrimPrefix(ref.PackageDigest, "sha256:")
}
func ToolCatalogID(ref core.PackageRef, provider string) string {
	return "st_" + strings.TrimPrefix(core.DigestBytes([]byte(ref.PackageDigest+"/"+provider)), "sha256:")
}
func (s *Service) catalogPackage(ref core.PackageRef) (*core.Package, error) {
	s.catalogMu.Lock()
	defer s.catalogMu.Unlock()
	if p := s.metadata[ref]; p != nil {
		return p, nil
	}
	p, err := s.Store.Load(ref)
	if err != nil {
		return nil, err
	}
	if s.metadata == nil {
		s.metadata = map[core.PackageRef]*core.Package{}
	}
	s.metadata[ref] = p
	return p, nil
}
func (s *Service) SelectionCatalog() SelectionCatalog {
	execution := s.ExecutionStatus()
	out := SelectionCatalog{Skills: []SkillEntry{}, Tools: []ToolEntry{}, ExecutionConfigured: execution.Configured}
	for _, installed := range s.Catalog() {
		entry := SkillEntry{ID: PackageSkillID(installed.Ref), Name: installed.Name, Source: "bundle", Package: installed.Ref, PackageName: installed.Name, State: "available", Issues: []SelectionIssue{}, Removable: true, PackageSizeBytes: installed.PackageSizeBytes, InstalledAt: installed.InstalledAt}
		p, err := s.catalogPackage(installed.Ref)
		if err != nil {
			entry.State = "unavailable"
			entry.Issues = append(entry.Issues, SelectionIssue{"INTEGRITY_FAILED", "Skill 内容校验失败，请重新安装"})
		} else {
			entry.SkillID, entry.Description = p.Manifest.Skill.ID, p.Manifest.Skill.Description
			entry.Required = p.Manifest.RequiresCapabilities
			entry.ToolCount = len(entry.Required)
			entry.RuntimeCount, entry.AssetCount = len(p.Manifest.Runtimes), len(p.Manifest.Assets)
			for _, provider := range p.Manifest.Providers {
				slots := []core.ResourceSlot{}
				for _, id := range provider.ResourceSlots {
					for _, slot := range p.Manifest.ResourceSlots {
						if slot.ID == id {
							slots = append(slots, slot)
						}
					}
				}
				out.Tools = append(out.Tools, ToolEntry{ID: ToolCatalogID(installed.Ref, provider.ID), Package: installed.Ref, PackageName: installed.Name, ProviderID: provider.ID, Contract: provider.Implements, Description: p.Contracts[provider.Implements.ID].Description, Revoked: installed.Revoked, ResourceSlots: slots, AssetIDs: append([]string(nil), provider.AssetIDs...)})
			}
			if !execution.Configured {
				entry.Issues = append(entry.Issues, SelectionIssue{Code: execution.IssueCode, Message: execution.Issue})
			}
			entry.Issues = append(entry.Issues, s.packagePlatformIssues(p)...)
			if len(entry.Issues) > 0 {
				entry.State = "needs_configuration"
			}
		}
		if installed.Revoked {
			entry.State = "revoked"
		}
		out.Skills = append(out.Skills, entry)
	}
	return out
}
func normalizeSelectionIDs(ids []string) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, id := range ids {
		if len(id) > 200 || strings.TrimSpace(id) != id || id == "" {
			return nil, errors.New("无效的 Skill ID")
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out, nil
}
func (s *Service) PlanSelection(input Selection) (SelectionPlan, error) {
	out := SelectionPlan{Execution: s.ExecutionStatus(), Issues: []SelectionIssue{}, Tools: []ToolEntry{}, Resources: []SelectionResource{}}
	var err error
	input.SkillIDs, err = normalizeSelectionIDs(input.SkillIDs)
	if err != nil {
		return out, err
	}
	out.Selection = input
	if input.Empty() {
		out.Ready = true
		return out, nil
	}
	catalog := s.SelectionCatalog()
	byID := map[string]SkillEntry{}
	for _, sk := range catalog.Skills {
		byID[sk.ID] = sk
	}
	b := core.Binding{APIVersion: core.APIVersion, Kind: "AgentBinding", AgentID: "agent:pending", Revision: 1, Packages: []core.PackageRef{}, Skills: []core.SkillRef{}, Capabilities: []core.CapabilityChoice{}, Resources: []core.ResourceAssignment{}, Grants: []core.Grant{}, UpgradePolicy: "new-sessions-only"}
	packages := []*core.Package{}
	seenPackages, seenCapabilities := map[string]bool{}, map[string]bool{}
	for _, id := range input.SkillIDs {
		sk, ok := byID[id]
		if !ok || sk.State == "revoked" || sk.State == "unavailable" {
			return out, fmt.Errorf("Skill 已停用或不可用：%s", id)
		}
		if seenPackages[sk.Package.ID] {
			return out, fmt.Errorf("Skill %s 只能选择一个版本", sk.Name)
		}
		seenPackages[sk.Package.ID] = true
		for _, issue := range sk.Issues {
			duplicate := false
			for _, existing := range out.Issues {
				if existing == issue {
					duplicate = true
					break
				}
			}
			if !duplicate {
				out.Issues = append(out.Issues, issue)
			}
		}
		p, err := s.catalogPackage(sk.Package)
		if err != nil {
			return out, err
		}
		packages = append(packages, p)
		b.Packages = append(b.Packages, sk.Package)
		b.Skills = append(b.Skills, core.SkillRef{PackageID: sk.Package.ID, SkillID: p.Manifest.Skill.ID})
		neededSlots := map[string]bool{}
		for _, provider := range p.Manifest.Providers {
			if seenCapabilities[provider.Implements.ID] {
				return out, fmt.Errorf("所选 Skill 重复提供能力 %s，请只保留一个提供该能力的 Skill", provider.Implements.ID)
			}
			seenCapabilities[provider.Implements.ID] = true
			if provider.Driver != core.DriverProcessJSON || provider.Requests == nil || p.Contracts[provider.Implements.ID].Effects.MutatesExternalState {
				return out, errors.New("所选工具不支持当前隔离执行环境")
			}
			if provider.Requests.Network != "none" {
				return out, errors.New("所选工具要求未获授权的网络访问")
			}
			profile, err := s.selectionProfile(provider.Requests.Limits)
			if err != nil {
				return out, err
			}
			pr := core.ProviderRef{PackageID: sk.Package.ID, ProviderID: provider.ID}
			b.Capabilities = append(b.Capabilities, core.CapabilityChoice{Contract: provider.Implements, Provider: pr})
			permissions := *provider.Requests
			b.Grants = append(b.Grants, core.Grant{Mode: core.GrantModeSandbox, Provider: pr, IsolationProfileRef: profile, Ceiling: &permissions})
			for _, tool := range catalog.Tools {
				if tool.Package == sk.Package && tool.ProviderID == provider.ID {
					out.Tools = append(out.Tools, tool)
				}
			}
			for _, slot := range provider.ResourceSlots {
				neededSlots[slot] = true
			}
		}
		for _, slot := range p.Manifest.ResourceSlots {
			if !neededSlots[slot.ID] {
				continue
			}
			ref := ""
			switch slot.Kind {
			case core.SlotKindFiles:
				ref = "files:current-session"
			case core.SlotKindArtifacts:
				ref = "artifacts:session-private"
			default:
				return out, errors.New("数据必须随 Skill 打包；不支持外部数据资源槽")
			}
			out.Resources = append(out.Resources, SelectionResource{PackageID: sk.Package.ID, SlotID: slot.ID, Kind: slot.Kind, Description: slot.Description, ResourceRef: ref})
			b.Resources = append(b.Resources, core.ResourceAssignment{PackageID: sk.Package.ID, SlotID: slot.ID, ResourceRef: ref})
		}
	}
	if err := core.ValidateBinding(b, packages); err != nil {
		return out, err
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return out, err
	}
	if err := core.ValidateDocument(core.KindAgentBinding, raw); err != nil {
		return out, err
	}
	out.Binding = raw
	out.Ready = len(out.Issues) == 0
	return out, nil
}
