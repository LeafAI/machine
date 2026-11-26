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
 for i:=0;i<count;i++{wg.Add(1);go func(i int){defer wg.Done()
  s:=fmt.Sprint(i);call:=requestContext(ctx,"session-"+s,"request-"+s)
  if e:=c.Write(call,&jsonrpc.Request{ID:id,Method:"tools/call",Params:json.RawMessage(fmt.Sprintf("%q",s))});e!=nil{errs<-e;return}
  m,e:=c.Read(call);if e!=nil{errs<-e;return}
  resp,ok:=m.(*jsonrpc.Response);if !ok||resp.ID!=id||string(resp.Result)!=fmt.Sprintf("%q",s){errs<-fmt.Errorf("wrong response for %s: %v",s,m)}
 }(i)}
 go func(){
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
