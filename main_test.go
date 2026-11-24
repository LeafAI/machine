package main

import (
 "context"
 "net/http"
 "net/http/httptest"
 "testing"
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
