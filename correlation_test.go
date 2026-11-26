package main

import (
 "context"
 "encoding/json"
 "fmt"
 "sync"
 "testing"
 "time"

 "github.com/modelcontextprotocol/go-sdk/jsonrpc"
 "github.com/modelcontextprotocol/go-sdk/mcp"
 "github.com/openai/tunnel-client/pkg/tunnelctx"
 "github.com/openai/tunnel-client/pkg/mcpclient"
 "os"
 "path/filepath"
 "strings"
)

func requestContext(ctx context.Context,session,request string)context.Context{
 return tunnelctx.ContextWithRequestID(tunnelctx.ContextWithSessionID(ctx,session),request)
}
func TestTunnelCorrelationConcurrentReusedIDs(t *testing.T){
 ctx,cancel:=context.WithTimeout(context.Background(),5*time.Second);defer cancel()
 st,ct:=mcp.NewInMemoryTransports()
 peer,e:=st.Connect(ctx);if e!=nil{t.Fatal(e)};defer peer.Close()
 c,e:=(&correlationTransport{base:ct,ctx:ctx}).Connect(ctx);if e!=nil{t.Fatal(e)};defer c.Close()
 const count=40
 id,_:=jsonrpc.MakeID(float64(1))
 var wg sync.WaitGroup
 errs:=make(chan error,count+1)
 wg.Add(1)
 for i:=0;i<count;i++{wg.Add(1);go func(i int){defer wg.Done()
  s:=fmt.Sprint(i);call:=requestContext(ctx,"session-"+s,"request-"+s)
  if e:=c.Write(call,&jsonrpc.Request{ID:id,Method:"tools/call",Params:json.RawMessage(fmt.Sprintf("%q",s))});e!=nil{errs<-e;return}
  m,e:=c.Read(call);if e!=nil{errs<-e;return}
  resp,ok:=m.(*jsonrpc.Response);if !ok||resp.ID!=id||string(resp.Result)!=fmt.Sprintf("%q",s){errs<-fmt.Errorf("wrong response for %s: %v",s,m)}
 }(i)}
 go func(){
  defer wg.Done()
  var reqs []*jsonrpc.Request
  ids:=map[jsonrpc.ID]bool{}
  for i:=0;i<count;i++{m,e:=peer.Read(ctx);if e!=nil{errs<-e;return};q:=m.(*jsonrpc.Request);if ids[q.ID]||q.ID==id{errs<-fmt.Errorf("downstream ID collision");return};ids[q.ID]=true;reqs=append(reqs,q)}
  for i:=len(reqs)-1;i>=0;i--{q:=reqs[i];if e:=peer.Write(ctx,&jsonrpc.Response{ID:q.ID,Result:q.Params});e!=nil{errs<-e;return}}
 }()
 wg.Wait();close(errs);for e:=range errs{t.Error(e)}
}
func TestTunnelCorrelationDropsLateResponse(t *testing.T){
 ctx,cancel:=context.WithTimeout(context.Background(),5*time.Second);defer cancel()
 st,ct:=mcp.NewInMemoryTransports();peer,_:=st.Connect(ctx);defer peer.Close()
 c,_:=(&correlationTransport{base:ct,ctx:ctx}).Connect(ctx);defer c.Close()
 id,_:=jsonrpc.MakeID("same")
 old,stop:=context.WithCancel(requestContext(ctx,"A","old"));defer stop()
 newCall:=requestContext(ctx,"B","new")
 peerErr:=make(chan error,1)
 go func(){
  m,e:=peer.Read(ctx);if e!=nil{peerErr<-e;return};a:=m.(*jsonrpc.Request)
  m,e=peer.Read(ctx);if e!=nil{peerErr<-e;return};b:=m.(*jsonrpc.Request)
  if a.ID==b.ID{peerErr<-fmt.Errorf("alias reused");return}
  if e=peer.Write(ctx,&jsonrpc.Response{ID:a.ID,Result:json.RawMessage(`"OLD"`)});e!=nil{peerErr<-e;return}
  peerErr<-peer.Write(ctx,&jsonrpc.Response{ID:b.ID,Result:json.RawMessage(`"NEW"`)})
 }()
 if e:=c.Write(old,&jsonrpc.Request{ID:id,Method:"tools/call"});e!=nil{t.Fatal(e)}
 stop()
 if _,e:=c.Read(old);e==nil{t.Fatal("canceled call returned a response")}
 if e:=c.Write(newCall,&jsonrpc.Request{ID:id,Method:"tools/call"});e!=nil{t.Fatal(e)}
 m,e:=c.Read(newCall);if e!=nil{t.Fatal(e)}
 if r:=m.(*jsonrpc.Response);r.ID!=id||string(r.Result)!=`"NEW"`{t.Fatal("late response crossed request boundary")}
 if e=<-peerErr;e!=nil{t.Fatal(e)}
 if _,e=c.Read(ctx);e==nil{t.Fatal("unowned read accepted")}
}

func TestTunnelCorrelationThroughSDKSharedTransport(t *testing.T){
 ctx,cancel:=context.WithTimeout(context.Background(),10*time.Second);defer cancel()
 path:=filepath.Join(t.TempDir(),"machine.yaml")
 if e:=os.WriteFile(path,[]byte("auth_required: false\nmax_concurrent: 4\n"),0600);e!=nil{t.Fatal(e)}
 cfg,e:=loadConfig(path);if e!=nil{t.Fatal(e)}
 srv:=newMachineServer(cfg,path)
 st,ct:=mcp.NewInMemoryTransports()
 session,e:=srv.Connect(ctx,st,nil);if e!=nil{t.Fatal(e)};defer session.Close()
 transport:=mcpclient.NewForwardingTransport(mcpclient.NewSharedConnectionTransport(&correlationTransport{base:ct,ctx:ctx}))
 id,_:=jsonrpc.MakeID(float64(1))
 call:=func(session,request,method string,params string)(*jsonrpc.Response,error){
  cctx:=requestContext(ctx,session,request)
  conn,e:=transport.Connect(cctx);if e!=nil{return nil,e}
  _,e=conn.Write(cctx,nil,&jsonrpc.Request{ID:id,Method:method,Params:json.RawMessage(params)});if e!=nil{return nil,e}
  msg,e:=conn.Read(cctx);if e!=nil{return nil,e}
  resp,ok:=msg.(*jsonrpc.Response);if !ok||resp.ID!=id{return nil,fmt.Errorf("unexpected RPC response ID")}
  if resp.Error!=nil{return nil,resp.Error}
  return resp,nil
 }
 if _,e=call("init","initialize","initialize",`{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}`);e!=nil{t.Fatal(e)}
 nctx:=requestContext(ctx,"init","notification")
 conn,e:=transport.Connect(nctx);if e!=nil{t.Fatal(e)};defer conn.Close()
 if _,e=conn.Write(nctx,nil,&jsonrpc.Request{Method:"notifications/initialized"});e!=nil{t.Fatal(e)}
 var wg sync.WaitGroup;ids:=make(chan string,2);errs:=make(chan error,2)
 for i:=0;i<2;i++{wg.Add(1);go func(i int){defer wg.Done()
  marker:=fmt.Sprintf("marker-%d",i)
  cmd:="printf "+marker;if i==0{cmd="sleep 0.2; "+cmd}
  p,_:=json.Marshal(map[string]any{"name":"shell","arguments":map[string]string{"command":cmd}})
  // The two sessions deliberately reuse the same RPC and request ID.
  resp,e:=call(fmt.Sprint(i),"same-request","tools/call",string(p))
  if e!=nil{errs<-e;return}
  var result struct{StructuredContent Output `json:"structuredContent"`}
  if e=json.Unmarshal(resp.Result,&result);e!=nil{errs<-e;return}
  out:=result.StructuredContent
  if out.Stdout!=marker||out.ExitCode!=0||!strings.HasPrefix(out.ToolCallID,"call_"){errs<-fmt.Errorf("wrong tool result for %s: %+v",marker,out);return}
  if responseToolCallID(resp)!=out.ToolCallID{errs<-fmt.Errorf("tool call ID correlation mismatch");return}
  ids<-out.ToolCallID
 }(i)}
 wg.Wait();close(errs);close(ids)
 for e:=range errs{t.Error(e)}
 seen:=map[string]bool{};for id:=range ids{if seen[id]{t.Fatal("tool call ID reused")};seen[id]=true}
 if len(seen)!=2{t.Fatal("missing tool call IDs")}
}
