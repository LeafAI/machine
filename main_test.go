package main

import (
 "context"
 "strings"
 "time"
 "github.com/modelcontextprotocol/go-sdk/mcp"
 "net/http"
 "net/http/httptest"
 "testing"
 "os"
 "path/filepath"
)
func TestAuth(t *testing.T) {
 h:=protect("secret",http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){w.WriteHeader(204)}))
 for _,v:=range []struct{token,origin string;want int}{{"","",401},{"Bearer wrong","",401},{"Bearer secret","",204},{"Bearer secret","https://evil.example",403}}{
  r:=httptest.NewRequest("POST","http://127.0.0.1:6666/mcp",nil);r.RemoteAddr="127.0.0.1:1234"
  if v.token!=""{r.Header.Set("Authorization",v.token)}
  if v.origin!=""{r.Header.Set("Origin",v.origin)}
  w:=httptest.NewRecorder();h.ServeHTTP(w,r)
  if w.Code!=v.want{t.Fatalf("got %d want %d",w.Code,v.want)}
 }
}
func TestShell(t *testing.T){
 c:=Config{Shell:"/bin/sh",TimeoutSeconds:2,MaxOutputBytes:4}
 o:=run(context.Background(),c,Input{Command:"printf abcdef; printf xyz >&2; exit 7"})
 if o.Stdout!="abcd"||o.Stderr!="xyz"||o.ExitCode!=7||!o.Truncated{t.Fatalf("%+v",o)}
}

func TestNoAuth(t *testing.T) {
 h:=protectWithAuth(false,"",http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){w.WriteHeader(204)}))
 for _,v:=range []struct{host,remote,origin string;want int}{
  {"127.0.0.1:6666","127.0.0.1:1234","",204},
  {"localhost:6666","[::1]:1234","",204},
  {"127.0.0.1:6666","192.0.2.1:1234","",403},
  {"evil.example:6666","127.0.0.1:1234","",403},
  {"127.0.0.1:6666","127.0.0.1:1234","https://evil.example",403},
 } {
  r:=httptest.NewRequest("POST","http://"+v.host+"/mcp",nil);r.RemoteAddr=v.remote
  if v.origin!=""{r.Header.Set("Origin",v.origin)}
  w:=httptest.NewRecorder();h.ServeHTTP(w,r)
  if w.Code!=v.want{t.Fatalf("%+v: got %d",v,w.Code)}
 }
}
func TestAuthConfig(t *testing.T){
 for _,v:=range []struct{yaml string;ok,required bool}{
  {"auth_required: false\n",true,false},
  {"shell: /bin/sh\n",false,true},
  {"auth_required: true\n",false,true},
  {"api_key: abcdef0123456789abcdef0123456789\n",true,true},
  {"auth_required: false\nlisten: '0.0.0.0:6666'\n",false,false},
 }{
  p:=filepath.Join(t.TempDir(),"machine.yaml")
  if err:=os.WriteFile(p,[]byte(v.yaml),0600);err!=nil{t.Fatal(err)}
  c,err:=loadConfig(p)
  if (err==nil)!=v.ok||c.AuthRequired!=v.required{t.Fatalf("%q: %+v %v",v.yaml,c,err)}
 }
}

func TestShellToolResultNonZeroExit(t *testing.T) {
 c:=Config{Shell:"/bin/sh",TimeoutSeconds:2,MaxOutputBytes:1024}
 result,out,err:=shellToolResult(context.Background(),c,Input{Command:"printf 'stdout-ok'; printf 'stderr-ok' >&2; exit 7"})
 if err!=nil||result==nil||result.IsError{t.Fatalf("command exit must not be an MCP error: %v",err)}
 if out.ExitCode!=7||out.Stdout!="stdout-ok"||out.Stderr!="stderr-ok"||out.TimedOut||out.Truncated{t.Fatalf("unexpected output: %+v",out)}
}

func TestShellToolResultSuccess(t *testing.T) {
 c:=Config{Shell:"/bin/sh",TimeoutSeconds:2,MaxOutputBytes:1024}
 result,out,err:=shellToolResult(context.Background(),c,Input{Command:"printf ok"})
 if err!=nil||result==nil||result.IsError||out.ExitCode!=0||out.Stdout!="ok"{t.Fatalf("unexpected result: %+v %v",out,err)}
}

func TestDynamicShellConfig(t *testing.T) {
 dir:=t.TempDir()
 p:=filepath.Join(dir,"machine.yaml")
 a:=filepath.Join(dir,"first")
 b:=filepath.Join(dir,"second")
 for _,d:=range []string{a,b}{
  if err:=os.Mkdir(d,0700);err!=nil{t.Fatal(err)}
 }
 if err:=os.WriteFile(filepath.Join(a,"machine-test-command"),[]byte("#!/bin/sh\nprintf first"),0700);err!=nil{t.Fatal(err)}
 if err:=os.WriteFile(filepath.Join(b,"machine-test-command"),[]byte("#!/bin/sh\nprintf second"),0700);err!=nil{t.Fatal(err)}
 write:=func(body string){t.Helper();if err:=os.WriteFile(p,[]byte(body),0600);err!=nil{t.Fatal(err)}}
 for _,v:=range []struct{dir,want string}{{a,"first"},{b,"second"}}{
  write("auth_required: false\nadditional_paths:\n  - '"+v.dir+"'\n")
  result,out,err:=shellToolFromConfig(context.Background(),p,Input{Command:"machine-test-command"})
  if err!=nil||result==nil||result.IsError||out.ExitCode!=0||out.Stdout!=v.want{t.Fatalf("unexpected result: %+v %v",out,err)}
 }
 write("auth_required: false\nadditional_paths: [\n")
 result,out,err:=shellToolFromConfig(context.Background(),p,Input{Command:"printf should-not-run"})
 if err!=nil||result==nil||result.IsError||out.ExitCode!=-1||out.Error==""||out.Stdout!=""{t.Fatalf("invalid configuration must prevent execution: %+v %v",out,err)}
 write("auth_required: false\n")
 _,out,err=shellToolFromConfig(context.Background(),p,Input{Command:"printf recovered"})
 if err!=nil||out.ExitCode!=0||out.Stdout!="recovered"{t.Fatalf("configuration recovery failed: %+v %v",out,err)}
}

func TestConfigHardening(t *testing.T){
 for _,body:=range []string{"auth_required: false\n---\nfoo: bar\n","auth_required: false\nlisten: '127.0.0.1:0'\n","auth_required: false\n"+strings.Repeat("#",1<<20)}{
  p:=filepath.Join(t.TempDir(),"machine.yaml")
  if err:=os.WriteFile(p,[]byte(body),0600);err!=nil{t.Fatal(err)}
  if _,err:=loadConfig(p);err==nil{t.Fatal("invalid config accepted")}
 }
}
func TestShellTimeout(t *testing.T){
 c:=Config{Shell:"/bin/sh",TimeoutSeconds:1,MaxOutputBytes:1024}
 start:=time.Now()
 out:=run(context.Background(),c,Input{Command:"printf started; sleep 30"})
 if !out.TimedOut||out.ExitCode==0||out.Stdout!="started"||time.Since(start)>5*time.Second{t.Fatalf("unexpected timeout: %+v",out)}
}
func TestMCPWire(t *testing.T){
 p:=filepath.Join(t.TempDir(),"machine.yaml")
 if err:=os.WriteFile(p,[]byte("auth_required: false\n"),0600);err!=nil{t.Fatal(err)}
 c,err:=loadConfig(p);if err!=nil{t.Fatal(err)}
 server:=newMachineServer(c,p)
 st,ct:=mcp.NewInMemoryTransports()
 ctx,cancel:=context.WithTimeout(context.Background(),10*time.Second);defer cancel()
 go func(){_=server.Run(ctx,st)}()
 client:=mcp.NewClient(&mcp.Implementation{Name:"test",Version:"1"},nil)
 session,err:=client.Connect(ctx,ct,nil);if err!=nil{t.Fatal(err)};defer session.Close()
 tools,err:=session.ListTools(ctx,nil);if err!=nil{t.Fatal(err)}
 if len(tools.Tools)!=1||tools.Tools[0].Name!="shell"{t.Fatal("unexpected tools")}
 result,err:=session.CallTool(ctx,&mcp.CallToolParams{Name:"shell",Arguments:map[string]any{"command":"printf stdout; printf stderr >&2; exit 7"}})
 if err!=nil||result==nil||result.IsError||result.StructuredContent==nil{t.Fatalf("unexpected MCP result: %v",err)}
}
