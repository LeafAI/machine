package main

import (
 "reflect"
 "context"
 "errors"
 "time"
 "github.com/modelcontextprotocol/go-sdk/mcp"
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

func TestTunnelSupervisorRetries(t *testing.T){
 ctx,cancel:=context.WithTimeout(context.Background(),time.Second);defer cancel()
 calls:=0;var delays []time.Duration
 superviseTunnel(ctx,func(ctx context.Context)error{
  calls++
  if calls==4{cancel();return ctx.Err()}
  return errors.New("simulated connection failure")
 },func(err error,d time.Duration){delays=append(delays,d)},time.Millisecond,2*time.Millisecond)
 if calls!=4||!reflect.DeepEqual(delays,[]time.Duration{time.Millisecond,2*time.Millisecond,2*time.Millisecond}){t.Fatalf("calls=%d delays=%v",calls,delays)}
}
func TestTunnelSupervisorCancelsBackoff(t *testing.T){
 ctx,cancel:=context.WithCancel(context.Background());defer cancel()
 calls:=0;start:=time.Now()
 superviseTunnel(ctx,func(context.Context)error{calls++;return errors.New("offline")},func(error,time.Duration){cancel()},time.Hour,time.Hour)
 if calls!=1||time.Since(start)>time.Second{t.Fatal("shutdown did not interrupt backoff")}
}
func TestTunnelSessionLossDoesNotDisableServer(t *testing.T){
 ctx,cancel:=context.WithTimeout(context.Background(),5*time.Second);defer cancel()
 server:=mcp.NewServer(&mcp.Implementation{Name:"test",Version:"1"},nil)
 for i:=0;i<2;i++{
  st,ct:=mcp.NewInMemoryTransports()
  ss,e:=server.Connect(ctx,st,nil);if e!=nil{t.Fatal(e)}
  client:=mcp.NewClient(&mcp.Implementation{Name:"test",Version:"1"},nil)
  cs,e:=client.Connect(ctx,ct,nil);if e!=nil{t.Fatal(e)}
  if _,e=cs.ListTools(ctx,nil);e!=nil{t.Fatal(e)}
  if e=cs.Close();e!=nil{t.Fatal(e)}
  done:=make(chan error,1);go func(){done<-ss.Wait()}()
  select{case <-done:case <-ctx.Done():t.Fatal("session did not close")}
  _=ss.Close()
 }
}
