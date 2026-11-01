package main

import (
	"encoding/json"
	"strings"
	"os"
	"path/filepath"
	"testing"
)

const fixtureAttck = `{
 "note": "演示快照",
 "entries": [
  {"id": "T1547.001", "kind": "technique", "name": "Registry Run Keys / Startup Folder", "zhName": "注册表 Run 键/启动文件夹", "tactics": ["Persistence", "Defense Evasion"], "platform": "Windows", "description": "登录自启动驻留"},
  {"id": "T1059.001", "kind": "technique", "name": "PowerShell", "tactics": ["Execution"], "platform": "Windows", "description": "脚本解释器滥用"},
  {"id": "T1566.001", "kind": "technique", "name": "Spearphishing Attachment", "zhName": "鱼叉钓鱼附件", "tactics": ["Initial Access"], "description": "恶意附件投递"},
  {"id": "S0650", "kind": "malware", "name": "QakBot", "aliases": ["QBot", "PinkSlip Bot"], "description": "银行木马"},
  {"id": "G0069", "kind": "group", "name": "Kimsuky", "aliases": ["Baby Coin"], "zhName": "Kimsuky 组织", "description": "朝鲜 APT"}
 ]
}`

const fixtureTiweb = `{
 "note": "tiweb 演示数据",
 "urlhaus": {"rows": 62834, "geoRows": 44087, "firstSeen": "2018-06-05", "lastSeen": "2026-08-19",
   "topCountries": [{"country": "China", "count": 21100}, {"country": "United States", "count": 7000}],
   "monthly": [{"month": "2018-06", "count": 642}]},
 "ransomwatch": {"rows": 16072, "groups": 157, "firstSeen": "2020-01-01", "lastSeen": "2026-08-19",
   "yearly": [{"year": 2020, "count": 1200}]},
 "ioc": {"domains": 581954, "ips": 21363}
}`

func writeFixture(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAttckLookupByIDAndZhName(t *testing.T) {
	p := writeFixture(t, "entries.json", fixtureAttck)
	// 精确编号
	out, err := attckLookup(p, json.RawMessage(`{"query":"T1547.001"}`))
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Count   int          `json:"count"`
		Entries []attckEntry `json:"entries"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatal(err)
	}
	if res.Count < 1 || res.Entries[0].ID != "T1547.001" {
		t.Fatalf("id lookup: %+v", res)
	}
	if len(res.Entries[0].Tactics) != 2 {
		t.Fatalf("tactics missing: %+v", res.Entries[0])
	}
	// 中文名查询(鱼叉钓鱼附件 → T1566.001)
	out, err = attckLookup(p, json.RawMessage(`{"query":"鱼叉钓鱼附件"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatal(err)
	}
	if res.Count != 1 || res.Entries[0].ID != "T1566.001" {
		t.Fatalf("zh lookup: %+v", res)
	}
	// 别名查询
	out, err = attckLookup(p, json.RawMessage(`{"query":"qbot","kind":"malware"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatal(err)
	}
	if res.Count != 1 || res.Entries[0].ID != "S0650" {
		t.Fatalf("alias lookup: %+v", res)
	}
	// 空查询拒绝
	if _, err := attckLookup(p, json.RawMessage(`{"query":"  "}`)); err == nil {
		t.Fatal("empty query accepted")
	}
	// 非法 kind 拒绝
	if _, err := attckLookup(p, json.RawMessage(`{"query":"x","kind":"campaign"}`)); err == nil {
		t.Fatal("bad kind accepted")
	}
}

func TestTiwebStats(t *testing.T) {
	p := writeFixture(t, "stats.json", fixtureTiweb)
	out, err := tiwebStats(p, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var snap tiwebSnapshot
	if err := json.Unmarshal(out, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.URLhaus.Rows != 62834 || snap.URLhaus.TopCountries[0].Country != "China" {
		t.Fatalf("urlhaus aggregates: %+v", snap.URLhaus)
	}
	if snap.Ransomwatch.Groups != 157 || snap.IOC.Domains != 581954 || snap.IOC.IPs != 21363 {
		t.Fatalf("ransomwatch/ioc aggregates: %+v", snap)
	}
}

func TestDispatchProtocol(t *testing.T) {
	// 无契约 describe 自述两个契约(带编译期摘要);带契约则精确回显;未知类型报 UNSUPPORTED_OPERATION。
	resp := dispatch(processRequest{Type: "describe"})
	if resp.Type != "describe" || len(resp.Operations) != 2 || resp.Operations[0].Contract.Digest == "" {
		t.Fatalf("self-describe: %+v", resp)
	}
	pinned := attckContract
	pinned.Digest = "sha256:" + strings.Repeat("a", 64)
	resp = dispatch(processRequest{Type: "describe", Contract: &pinned})
	if resp.Type != "describe" || len(resp.Operations) != 1 || resp.Operations[0].Contract.Digest != pinned.Digest {
		t.Fatalf("pinned describe: %+v", resp)
	}
	resp = dispatch(processRequest{Type: "call"})
	if resp.Type != "error" || resp.Error.Code != "UNSUPPORTED_OPERATION" {
		t.Fatalf("unknown type must be UNSUPPORTED_OPERATION: %+v", resp)
	}
	resp = dispatch(processRequest{Type: "invoke", Contract: &contractIdentity{ID: "malware.nope", Version: "9"}})
	if resp.Error == nil || resp.Error.Code != "UNSUPPORTED_OPERATION" {
		t.Fatalf("unknown contract: %+v", resp)
	}
}
