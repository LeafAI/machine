package main

import (
 "context"
 "net/http"
 "net/http/httptest"
 "testing"
 "os"
 "path/filepath"
)
func TestAuth(t *testing.T) {
 h:=protect("secret",http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){w.WriteHeader(204)}))
 for _,v:=range []struct{token,origin string;want int}{{"","",401},{"Bearer wrong","",401},{"Bearer secret","",204},{"Bearer secret","https://evil.example",403}}{
  r:=httptest.NewRequest("POST","/mcp",nil)
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
