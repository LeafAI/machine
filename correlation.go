package main

import (
 "context"
 "crypto/rand"
 "encoding/json"
 "errors"
 "fmt"
 "io"
 "log/slog"
 "sync"

 "github.com/modelcontextprotocol/go-sdk/jsonrpc"
 "github.com/modelcontextprotocol/go-sdk/mcp"
 "github.com/openai/tunnel-client/pkg/tunnelctx"
)

// One physical connection per tunnel generation; one reader owns it.
// The SDK's shared in-memory transport otherwise allows concurrent workers
// to read each other's responses. RPC IDs alone are only session-local.
type correlationTransport struct {
 base mcp.Transport
 ctx context.Context
 logger *callLogger
}
func(t *correlationTransport) Connect(ctx context.Context)(mcp.Connection,error){
 base,e:=t.base.Connect(ctx);if e!=nil{return nil,e}
 c:=&correlationConn{base:base,ctx:t.ctx,logger:t.logger,generation:rand.Text(),closed:make(chan struct{}),byKey:make(map[requestKey]*pendingRequest),byAlias:make(map[jsonrpc.ID]*pendingRequest)}
 go c.dispatch()
 return c,nil
}
type requestKey struct{shard,request string}
func keyFromContext(ctx context.Context)(requestKey,error){
 id,ok:=tunnelctx.RequestIDFromContext(ctx)
 if !ok{return requestKey{},errors.New("tunnel correlation: missing upstream request ID")}
 shard,_:=tunnelctx.ShardTokenFromContext(ctx)
 return requestKey{shard,id},nil
}
type pendingRequest struct{
 key requestKey
 session string
 original,alias jsonrpc.ID
 response chan *jsonrpc.Response
 stop func()bool
}
type correlationConn struct{
 base mcp.Connection
 ctx context.Context
 logger *callLogger
 generation string
 mu sync.Mutex
 byKey map[requestKey]*pendingRequest
 byAlias map[jsonrpc.ID]*pendingRequest
 closed chan struct{}
 once sync.Once
}
func(c *correlationConn) SessionID()string{return c.base.SessionID()}
func(c *correlationConn) Close()error{
 c.once.Do(func(){
  c.mu.Lock()
  close(c.closed)
  for _,p:=range c.byKey{if p.stop!=nil{p.stop()}}
  clear(c.byKey);clear(c.byAlias)
  c.mu.Unlock()
  _=c.base.Close()
 })
 return nil
}
func(c *correlationConn) remove(p *pendingRequest){
 c.mu.Lock();defer c.mu.Unlock()
 if c.byKey[p.key]==p{delete(c.byKey,p.key)}
 if c.byAlias[p.alias]==p{delete(c.byAlias,p.alias)}
 if p.stop!=nil{p.stop()}
}
func(c *correlationConn) Write(ctx context.Context,msg jsonrpc.Message)error{
 if e:=ctx.Err();e!=nil{return e}
 req,ok:=msg.(*jsonrpc.Request);if !ok{return errors.New("tunnel correlation: unsupported upstream message")}
 key,e:=keyFromContext(ctx);if e!=nil{return e}
 session,_:=tunnelctx.SessionIDFromContext(ctx)
 if !req.ID.IsValid(){
  if req.Method=="notifications/cancelled"{return c.cancelNotification(ctx,req,session)}
  return c.base.Write(ctx,req)
 }
 alias,e:=jsonrpc.MakeID("rpc_"+c.generation+"_"+rand.Text());if e!=nil{return e}
 p:=&pendingRequest{key:key,session:session,original:req.ID,alias:alias,response:make(chan *jsonrpc.Response,1)}
 c.mu.Lock()
 select{case <-c.closed:c.mu.Unlock();return io.ErrClosedPipe;default:}
 if len(c.byKey)>=128||c.byKey[key]!=nil||c.byAlias[alias]!=nil{c.mu.Unlock();return errors.New("tunnel correlation: duplicate request or capacity exhausted")}
 c.byKey[key]=p;c.byAlias[alias]=p
 p.stop=context.AfterFunc(ctx,func(){c.remove(p)})
 c.mu.Unlock()
 clone:=*req;clone.ID=alias
 if c.logger!=nil{c.logger.line(slog.LevelDebug,"tunnel request generation=%s request=%q session=%q rpc=%q alias=%q",c.generation,key.request,session,fmt.Sprint(req.ID.Raw()),fmt.Sprint(alias.Raw()))}
 if e=c.base.Write(ctx,&clone);e!=nil{c.remove(p);return e}
 return nil
}
// Cancellation must resolve within the caller's upstream session. Unknown or
// ambiguous notifications are ignored, never sent with an unscoped RPC ID.
func(c *correlationConn) cancelNotification(ctx context.Context,req *jsonrpc.Request,session string)error{
 if session==""{return nil}
 var params map[string]json.RawMessage
 if e:=json.Unmarshal(req.Params,&params);e!=nil{return e}
 var raw any
 if e:=json.Unmarshal(params["requestId"],&raw);e!=nil{return e}
 id,e:=jsonrpc.MakeID(raw);if e!=nil{return e}
 key,e:=keyFromContext(ctx);if e!=nil{return e}
 c.mu.Lock()
 var match *pendingRequest
 for _,p:=range c.byKey{if p.session==session&&p.key.shard==key.shard&&p.original==id{if match!=nil{c.mu.Unlock();return nil};match=p}}
 c.mu.Unlock()
 if match==nil{return nil}
 params["requestId"],e=json.Marshal(match.alias.Raw());if e!=nil{return e}
 clone:=*req;clone.Params,e=json.Marshal(params);if e!=nil{return e}
 return c.base.Write(ctx,&clone)
}
func(c *correlationConn) Read(ctx context.Context)(jsonrpc.Message,error){
 key,e:=keyFromContext(ctx);if e!=nil{return nil,e}
 c.mu.Lock();p:=c.byKey[key];c.mu.Unlock()
 if p==nil{if e=ctx.Err();e!=nil{return nil,e};return nil,errors.New("tunnel correlation: no pending request")}
 defer c.remove(p)
 select{
 case <-ctx.Done():return nil,ctx.Err()
 case <-c.closed:return nil,io.ErrClosedPipe
 case resp:=<-p.response:
  if e=ctx.Err();e!=nil{return nil,e}
  select{case <-c.closed:return nil,io.ErrClosedPipe;default:}
  return resp,nil
 }
}
func(c *correlationConn) dispatch(){
 defer c.Close()
 for{
  msg,e:=c.base.Read(c.ctx);if e!=nil{return}
  resp,ok:=msg.(*jsonrpc.Response)
  // No machine tool initiates server-to-client requests or progress
  // notifications. Without an ownership mapping these must not be sent
  // to whichever SDK worker happens to be reading.
  if !ok||!resp.ID.IsValid(){continue}
  c.mu.Lock()
  p:=c.byAlias[resp.ID]
  if p!=nil{
   delete(c.byAlias,resp.ID)
   clone:=*resp;clone.ID=p.original
   p.response<-&clone
  }
  c.mu.Unlock()
  if c.logger!=nil{
   if p==nil{c.logger.line(slog.LevelWarn,"tunnel dropped unmatched response generation=%s",c.generation)}else{
    c.logger.line(slog.LevelDebug,"tunnel response generation=%s request=%q session=%q rpc=%q alias=%q",c.generation,p.key.request,p.session,fmt.Sprint(p.original.Raw()),fmt.Sprint(p.alias.Raw()))
   }
  }
 }
}
