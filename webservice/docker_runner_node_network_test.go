package webservice

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// This runs in Linux runtime QA as well as Linux CI. It exercises the actual
// inherited kernel filter across exec, including a child process launched by
// Node; replacing JavaScript networking functions would not pass this test.
func TestManagedNodeNetworkFilterLinuxEnforcement(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("actual inherited seccomp enforcement requires Linux")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js unavailable")
	}
	const app = `
const net=require('node:net'),dgram=require('node:dgram'),http=require('node:http'),crypto=require('node:crypto'),cp=require('node:child_process');
function tcp(options){return new Promise(resolve=>{const s=net.connect(options);s.on('error',e=>resolve(e.code==='EPERM'));s.on('connect',()=>{s.destroy();resolve(false)});})}
async function main(){
 const checks={tcp:await tcp({host:'127.0.0.1',port:9}),unix:await tcp({path:'/tmp/pogo-no-such-socket'})};
 checks.udp=await new Promise(resolve=>{const s=dgram.createSocket('udp4');s.send(Buffer.from('x'),9,'127.0.0.1',e=>{resolve(e?.code==='EPERM');s.close()})});
 const child=cp.spawnSync(process.execPath,['-e',"require('node:net').connect({host:'127.0.0.1',port:9}).on('error',e=>process.exit(e.code==='EPERM'?0:3))"],{timeout:3000});
 checks.child=child.status===0;
 const server=http.createServer((req,res)=>{res.setHeader('Content-Type','application/json');res.end(JSON.stringify(checks))});
 server.on('upgrade',(req,socket)=>{
   const accept=crypto.createHash('sha1').update(req.headers['sec-websocket-key']+'258EAFA5-E914-47DA-95CA-C5AB0DC85B11').digest('base64');
   socket.write('HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: '+accept+'\r\n\r\n');
   let pending=Buffer.alloc(0);socket.on('data',data=>{pending=Buffer.concat([pending,data]);if(pending.length<6)return;const n=pending[1]&127;if(pending.length<6+n)return;const reply=Buffer.alloc(n);for(let i=0;i<n;i++)reply[i]=pending[6+i]^pending[2+(i%4)];socket.end(Buffer.concat([Buffer.from([129,n]),reply]));server.close();server.closeAllConnections();});
 });
 server.listen(0,'127.0.0.1',()=>console.log(JSON.stringify({port:server.address().port})));
}
main().catch(e=>{console.error(e.message);process.exit(2)});
`
	nodeJSON, _ := json.Marshal(node)
	argvJSON, _ := json.Marshal([]string{node, "-e", app})
	script := pythonNetworkGuard + "\nimport os\n_deny_outbound()\nos.execv(" + string(nodeJSON) + ", " + string(argvJSON) + ")\n"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, pythonForNetworkTest(t), "-I", "-c", script)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
	line, err := bufio.NewReader(stdout).ReadBytes('\n')
	if err != nil {
		t.Fatalf("guarded Node startup failed: %s %v", stderr.String(), err)
	}
	var ready struct{ Port int }
	if err := json.Unmarshal(line, &ready); err != nil || ready.Port < 1 {
		t.Fatalf("invalid Node readiness: %s %v", line, err)
	}
	address := fmt.Sprintf("127.0.0.1:%d", ready.Port)
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	res, err := client.Get("http://" + address + "/api/checks")
	if err != nil {
		t.Fatalf("inbound HTTP failed: %v %s", err, stderr.String())
	}
	var checks struct{ TCP, UDP, Unix, Child bool }
	err = json.NewDecoder(res.Body).Decode(&checks)
	res.Body.Close()
	if err != nil || res.StatusCode != 200 || !checks.TCP || !checks.UDP || !checks.Unix || !checks.Child {
		t.Fatalf("Node/descendant outbound isolation failed: %+v %v %s", checks, err, stderr.String())
	}
	transport.CloseIdleConnections()
	conn, err := net.DialTimeout("tcp", address, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	_, _ = io.WriteString(conn, "GET /ws HTTP/1.1\r\nHost: "+address+"\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n")
	reader := bufio.NewReader(conn)
	request, _ := http.NewRequest("GET", "http://"+address+"/ws", nil)
	upgrade, err := http.ReadResponse(reader, request)
	if err != nil || upgrade.StatusCode != 101 {
		t.Fatalf("Node WebSocket upgrade failed: %v", err)
	}
	frame := []byte{129, 132, 1, 2, 3, 4, 'P' ^ 1, 'I' ^ 2, 'N' ^ 3, 'G' ^ 4}
	_, _ = conn.Write(frame)
	reply := make([]byte, 6)
	if _, err := io.ReadFull(reader, reply); err != nil || !bytes.Equal(reply, []byte{129, 4, 'P', 'I', 'N', 'G'}) {
		t.Fatalf("Node WebSocket echo failed: %v %v", reply, err)
	}
	conn.Close()
	if err := command.Wait(); err != nil {
		t.Fatalf("guarded Node failed: %v %s", err, strings.TrimSpace(stderr.String()))
	}
}
