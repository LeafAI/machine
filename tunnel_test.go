package main

import (
 "reflect"
 "testing"
)

func TestTunnelConfig(t *testing.T){
 t.Setenv("CONTROL_PLANE_TUNNEL_ID","")
 t.Setenv("CONTROL_PLANE_API_KEY","")
 if err:=(TunnelConfig{}).validate();err!=nil{t.Fatal(err)}
 c:=TunnelConfig{Enabled:true}
 if c.validate()==nil{t.Fatal("missing tunnel ID accepted")}
 t.Setenv("CONTROL_PLANE_TUNNEL_ID","tunnel_0123456789abcdef0123456789abcdef")
 if c.validate()==nil{t.Fatal("missing runtime key accepted")}
 c.APIKey="yaml-runtime-key"
 yamlCfg,err:=c.sdkConfig()
 if err!=nil||yamlCfg.APIKey!="yaml-runtime-key"{t.Fatal("YAML runtime key not accepted")}
 t.Setenv("CONTROL_PLANE_API_KEY","test-runtime-key")
 cfg,err:=c.sdkConfig()
 if err!=nil||cfg.APIKey!="test-runtime-key"||cfg.TunnelID!="tunnel_0123456789abcdef0123456789abcdef"{t.Fatalf("invalid config: %v",err)}
 c.ID="invalid"
 if c.validate()==nil{t.Fatal("invalid ID accepted")}
}

func TestShellEnvironment(t *testing.T){
 got:=shellEnvironment([]string{"PATH=/bin","CONTROL_PLANE_API_KEY=secret","OPENAI_ADMIN_KEY=secret","OPENAI_API_KEY=secret","HOME=/tmp"})
 if !reflect.DeepEqual(got,[]string{"PATH=/bin","HOME=/tmp"}){t.Fatal("unexpected subprocess environment")}
}
