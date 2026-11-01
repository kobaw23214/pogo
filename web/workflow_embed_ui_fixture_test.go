package web

// The external host below is a test-only business system. Its two fixed actor
// URLs simulate login; production hosts must resolve subject from server-side
// authenticated sessions. The integration secret stays in this Go closure.
import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/taskqueue"
	"github.com/yosukeno/pogo/workflow"
)

func startWorkflowEmbedUIHost(t *testing.T, m *Manager, pogoURL, adminToken, agentID, output string) map[string]any {
	t.Helper()
	approvalMode := ""
	if os.Getenv("POGO_WORKFLOW_UI_STAGE4") == "1" {
		approvalMode = "fixed"
		if os.Getenv("POGO_WORKFLOW_UI_APPROVAL_MODE") == "business" {
			approvalMode = "business"
		}
	}
	businessMode := os.Getenv("POGO_WORKFLOW_UI_STAGE3") == "1" || approvalMode != ""
	reviewNode := workflow.Node{ID: "review", Kind: workflow.KindHuman, Prompt: "请审核订单 {{ inputs.order_id }}：{{ inputs.note }}", Fields: []workflow.FieldSpec{{Key: "comment", Label: "审批意见", Required: true, Type: "text"}}}
	if approvalMode != "" {
		reviewNode.ApprovalPolicy = &workflow.ApprovalPolicy{PreventSelfApproval: true, Assignment: approvalMode}
		if approvalMode == "fixed" {
			reviewNode.ApprovalPolicy.Approvers = []string{"alice", "bob"}
		}
	}
	wf, err := m.workflows.put("", workflowInput{Name: "订单审核嵌入验收", Description: "跨来源 iframe 独立验收", Nodes: []workflow.Node{
		reviewNode,
		{ID: "finish", Agent: agentID, Task: "UI_FIXTURE_EMBED：订单 {{ inputs.order_id }}；说明 {{ inputs.note }}。根据人工审批结果给出确认。", After: []workflow.Dep{{Node: "review"}}},
	}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	ptr, err := m.workflows.publish(wf.ID, "嵌入验收 v1", "固定发布版本与独立业务输入")
	if err != nil {
		t.Fatal(err)
	}
	control, err := m.taskRuns.database(true)
	if err != nil {
		t.Fatal(err)
	}
	bob := identity.LocalPrincipal(identity.LocalScope("bob"), "bob", false)
	if err := control.SetWorkflowGrant(wf.ID, bob.ID, []string{wfRead}); err != nil {
		t.Fatal(err)
	}
	var publicationID, clientSecret, hostURL string
	var callbacksMu sync.Mutex
	callbackAttempts := []map[string]any{}
	callbackAccepted := map[string]taskqueue.WorkflowCallbackEvent{}
	initialFailure := businessMode
	callbackLog := func() map[string]any {
		return map[string]any{"attempts": callbackAttempts, "accepted": callbackAccepted, "production_data": false, "signatures_verified": true}
	}
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if (r.URL.Path == "/callback" || r.URL.Path == "/callback-new") && r.Method == http.MethodPost {
			payload, err := io.ReadAll(io.LimitReader(r.Body, 65537))
			if err != nil || len(payload) > 65536 {
				http.Error(w, "invalid fixture callback", 400)
				return
			}
			var event taskqueue.WorkflowCallbackEvent
			if json.Unmarshal(payload, &event) != nil || event.ID != r.Header.Get("X-Pogo-Event-Id") {
				http.Error(w, "invalid event identity", 400)
				return
			}
			// Test-only receiver configuration follows the private fixture file,
			// never a browser-provided signing key. Real hosts configure their
			// own secret and atomically persist receipt IDs with business writes.
			store, err := openWorkflowPublications(m.cfg.SessionDir)
			if err != nil {
				http.Error(w, "receiver configuration unavailable", 503)
				return
			}
			p, ok := store.get(event.PublicationID)
			ts := r.Header.Get("X-Pogo-Timestamp")
			seconds, parseErr := strconv.ParseInt(ts, 10, 64)
			age := time.Now().Unix() - seconds
			mac := hmac.New(sha256.New, []byte(p.SigningSecret))
			_, _ = io.WriteString(mac, event.ID+"."+ts+".")
			_, _ = mac.Write(payload)
			given, decodeErr := hex.DecodeString(strings.TrimPrefix(r.Header.Get("X-Pogo-Signature"), "sha256="))
			if !ok || p.SigningSecret == "" || parseErr != nil || age < -300 || age > 300 || decodeErr != nil || !hmac.Equal(given, mac.Sum(nil)) {
				http.Error(w, "invalid signature", 401)
				return
			}
			callbacksMu.Lock()
			defer callbacksMu.Unlock()
			_, duplicate := callbackAccepted[event.ID]
			_, failFile := os.Stat(filepath.Join(output, "receiver-fail"))
			fail := initialFailure || failFile == nil
			initialFailure = false
			callbackAttempts = append(callbackAttempts, map[string]any{"event_id": event.ID, "run_id": event.RunID, "business_id": event.BusinessID, "type": event.Type, "sequence": event.Sequence, "duplicate": duplicate, "response_failed": fail})
			if !fail {
				callbackAccepted[event.ID] = event
			}
			encoded, _ := json.MarshalIndent(callbackLog(), "", "  ")
			if err := writeWorkflowFileAtomic(filepath.Join(output, "callback-receipts.json"), encoded); err != nil {
				http.Error(w, "fixture receipt persistence failed", 503)
				return
			}
			if fail {
				http.Error(w, "fixture receiver temporarily unavailable", 503)
				return
			}
			if _, err := os.Stat(filepath.Join(output, "receiver-drop-ack")); err == nil {
				_ = os.Remove(filepath.Join(output, "receiver-drop-ack"))
				if hijacker, ok := w.(http.Hijacker); ok {
					conn, _, e := hijacker.Hijack()
					if e == nil {
						_ = conn.Close()
						return
					}
				}
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path == "/callback-log" {
			callbacksMu.Lock()
			defer callbacksMu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(callbackLog())
			return
		}
		if r.URL.Path == "/monitor" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, workflowCallbackMonitorHTML)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/launch/") && r.Method == http.MethodPost {
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/launch/"), "/")
			actor := parts[0]
			if actor != "alice" && actor != "bob" {
				http.NotFound(w, r)
				return
			}
			businessID := "ORDER-" + strings.ToUpper(actor)
			if businessMode {
				if len(parts) != 2 || (parts[1] != "A" && parts[1] != "B") {
					http.NotFound(w, r)
					return
				}
				businessID = "ORDER-" + parts[1]
			}
			var selection struct {
				RunID string `json:"run_id"`
			}
			if r.Body != nil {
				_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&selection)
			}
			launch := map[string]any{"subject": "external-" + actor, "origin": hostURL, "run_id": selection.RunID, "business_id": businessID, "inputs": map[string]string{"order_id": businessID, "note": "来自独立业务系统的测试订单"}}
			if approvalMode == "business" && selection.RunID == "" {
				launch["approver_subjects"] = map[string][]string{"review": {"external-bob"}}
			}
			body, _ := json.Marshal(launch)
			req, _ := http.NewRequest(http.MethodPost, pogoURL+workflowEmbedPrefix+publicationID+"/launch", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+clientSecret)
			req.Header.Set("Content-Type", "application/json")
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				http.Error(w, "测试宿主暂时无法连接 Pogo", 502)
				return
			}
			defer res.Body.Close()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(res.StatusCode)
			_, _ = io.Copy(w, res.Body)
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
		actor := parts[0]
		if actor != "alice" && actor != "bob" {
			http.NotFound(w, r)
			return
		}
		actorPath := actor
		if businessMode {
			if len(parts) != 2 || (parts[1] != "A" && parts[1] != "B") {
				http.NotFound(w, r)
				return
			}
			actorPath += "/" + parts[1]
		}
		config, _ := json.Marshal(map[string]string{"origin": pogoURL, "publication_id": publicationID, "actor": actorPath})
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, workflowEmbedHostHTML, actor, string(config))
	}))
	t.Cleanup(host.Close)
	hostURL = host.URL
	historyScope := "workflow"
	if businessMode {
		historyScope = "business"
	}
	publicationConfig := map[string]any{"name": "外部订单系统", "version_id": ptr.VersionID, "enabled": true, "history_scope": historyScope, "allowed_origins": []string{host.URL},
		"mappings":     []map[string]string{{"subject": "external-alice", "username": "alice"}, {"subject": "external-bob", "username": "bob"}},
		"input_fields": []map[string]any{{"key": "order_id", "label": "订单编号", "required": true}, {"key": "note", "label": "订单说明", "required": true}}}
	if businessMode {
		publicationConfig["callback"] = map[string]any{"enabled": true, "url": host.URL + "/callback"}
	}
	payload, _ := json.Marshal(publicationConfig)
	req, _ := http.NewRequest(http.MethodPost, pogoURL+"/api/manage/workflows/"+wf.ID+"/publications", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var created struct {
		Publication  workflowPublication `json:"publication"`
		ClientSecret string              `json:"client_secret"`
	}
	if res.StatusCode != 201 {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("create embedded fixture publication: %d %s", res.StatusCode, b)
	}
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	publicationID, clientSecret = created.Publication.ID, created.ClientSecret
	result := map[string]any{"host_url": host.URL, "alice_url": host.URL + "/alice", "bob_url": host.URL + "/bob", "publication_id": publicationID, "workflow_id": wf.ID, "version_id": ptr.VersionID, "client_secret_in_browser": false, "history_scope": historyScope, "approval_mode": approvalMode}
	if businessMode {
		result["alice_a_url"] = host.URL + "/alice/A"
		result["alice_b_url"] = host.URL + "/alice/B"
		result["bob_a_url"] = host.URL + "/bob/A"
		result["callback_monitor_url"] = host.URL + "/monitor"
	}
	return result
}

const workflowCallbackMonitorHTML = `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><title>独立后台回调接收记录</title><style>body{font:15px system-ui;padding:24px;background:#f1f4f8;color:#234}table{border-collapse:collapse;background:white;width:100%}td,th{padding:12px;text-align:left;border:1px solid #ccd}h1{font-size:22px}code{font-size:12px}</style><h1>独立业务系统 · 后台回调接收记录</h1><p>本页仅显示独立测试接收器的记录。关闭工作流页面后，后台仍独立接收并按事件 ID 去重。</p><p id="summary">正在读取</p><table><thead><tr><th>业务单</th><th>事件</th><th>顺序</th><th>运行</th><th>稳定事件 ID</th></tr></thead><tbody id="events"></tbody></table><script>async function refresh(){const response=await fetch('/callback-log');const data=await response.json();document.getElementById('summary').textContent='已验签接收 '+Object.keys(data.accepted).length+' 个独立事件；共尝试 '+data.attempts.length+' 次';const table=document.getElementById('events');table.replaceChildren();for(const event of Object.values(data.accepted)){const row=document.createElement('tr');for(const text of [event.business_id,event.type,event.sequence,event.run_id,event.id]){const cell=document.createElement('td');cell.textContent=text;row.append(cell);}table.append(row);}}refresh();setInterval(refresh,1000);</script></html>`

const workflowEmbedHostHTML = `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><title>独立业务系统 · 工作流嵌入验收</title>
<style>body{margin:0;background:#eef1f5;font:15px system-ui;color:#253041}header{padding:18px 24px;background:#fff;box-shadow:0 1px 5px #ccd}h1{font-size:19px;margin:0 0 8px}p{margin:6px 0}#status{color:#465c92}button{padding:8px 16px;margin:8px 8px 0 0;cursor:pointer}iframe{display:block;margin:20px auto;width:calc(100%% - 48px);height:740px;border:1px solid #ccd;border-radius:12px;background:white}a{color:#3356a0}</style>
<header><h1>订单业务系统 · 当前测试账号 %s</h1><p>独立测试宿主：登录身份由本地夹具模拟，未连接真实业务系统。接入密钥只保存在宿主后端。</p><p id="status">等待工作流连接</p><button id="reload">重新载入工作流</button><a id="alice-link" href="/alice">切换 Alice</a> · <a id="bob-link" href="/bob">切换 Bob</a></header>
<iframe id="workflow" title="订单审核工作流" referrerpolicy="no-referrer"></iframe>
<script>const config=%s;const frame=document.getElementById('workflow');const status=document.getElementById('status');let active=0;let selectedRun=new URL(location.href).searchParams.get('pogo_run')||'';
const business=config.actor.split('/')[1];if(business){document.getElementById('alice-link').href='/alice/'+business;document.getElementById('bob-link').href='/bob/'+business;}
document.getElementById('reload').onclick=()=>{active++;frame.src=frame.src;status.textContent='正在重新连接';};
window.addEventListener('message',async event=>{if(event.origin!==config.origin||event.source!==frame.contentWindow)return;const data=event.data;if(!data||data.protocol!==1||data.publication_id!==config.publication_id)return;
if(data.type==='pogo.workflow.ready'){const turn=++active;status.textContent='宿主后端正在申请一次性启动码';try{const response=await fetch('/launch/'+config.actor,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({run_id:selectedRun})});const launch=await response.json();if(!response.ok)throw new Error(launch.error||'申请失败');if(turn!==active)return;frame.contentWindow.postMessage({type:'pogo.workflow.launch',protocol:1,publication_id:config.publication_id,request_id:data.request_id,code:launch.launch_code},config.origin);status.textContent='已发送一次性启动码';}catch(error){status.textContent=error.message;}}
if(data.type==='pogo.workflow.run'){status.textContent='当前运行 '+data.run_id+' · '+data.status;selectedRun=data.run_id;const url=new URL(location.href);url.searchParams.set('pogo_run',data.run_id);history.replaceState(history.state,'',url);}
});
frame.src=config.origin+'/embed/workflows/'+config.publication_id+'?parent_origin='+encodeURIComponent(location.origin)+(selectedRun?'&run='+encodeURIComponent(selectedRun):'');
</script></html>`
