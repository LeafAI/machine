package main

import (
 "context"
 "encoding/json"
 "errors"
 "fmt"
 "sync"
 "bytes"
 "time"

 "github.com/modelcontextprotocol/go-sdk/mcp"
)

type ToolResponse[T any] struct {
 Result T `json:"result"`
 Error string `json:"error,omitempty"`
}
type toolFunc[I,O any] func(context.Context,Config,I)(O,error)
func registerNative[I,O any](server *mcp.Server,start Config,path string,slots chan struct{},calls *callLogger,name,description string,fn toolFunc[I,O]){
 if !start.enabled(name){return}
 coordinators:=make(chan struct{},cap(slots))
 mcp.AddTool(server,&mcp.Tool{Name:name,Description:description},func(ctx context.Context,_ *mcp.CallToolRequest,in I)(result *mcp.CallToolResult,out ToolResponse[O],err error){
  finish:=calls.begin(name,in);defer func(){finish(err==nil&&out.Error=="","",out)}()
  if err:=ctx.Err();err!=nil{return nil,out,err}
  if name!="batch"{select{case slots<-struct{}{}:defer func(){<-slots}();default:out.Error="tool concurrency limit reached";return &mcp.CallToolResult{},out,nil}}else{select{case coordinators<-struct{}{}:defer func(){<-coordinators}();default:out.Error="batch concurrency limit reached";return &mcp.CallToolResult{},out,nil};ctx=context.WithValue(ctx,batchSlotsKey{},slots);ctx=context.WithValue(ctx,batchLoggerKey{},calls)}
  c,e:=loadConfig(path);if e!=nil{out.Error="invalid configuration";return &mcp.CallToolResult{},out,nil}
  // Registration is fixed until restart; enforce startup allowlist in batches.
  c.Tools=start.Tools
  ctx,cancel:=context.WithTimeout(ctx,time.Duration(c.TimeoutSeconds)*time.Second);defer cancel()
  out.Result,e=fn(ctx,c,in);if e!=nil{out.Error=e.Error()}
  return &mcp.CallToolResult{},out,nil
 })
}
func registerNativeTools(server *mcp.Server,c Config,path string,slots chan struct{},calls *callLogger){
 registerNative(server,c,path,slots,calls,"file_read","Read an explicit inclusive line range of a UTF-8 file; returns content and SHA-256.",fileRead)
 registerNative(server,c,path,slots,calls,"file_write","Create a UTF-8 file, or explicitly overwrite it. Supports conditional SHA-256.",fileWrite)
 registerNative(server,c,path,slots,calls,"file_edit","Replace an exact unique string or inclusive line range; optional expected SHA-256.",fileEdit)
 registerNative(server,c,path,slots,calls,"file_info","Get metadata for multiple paths.",fileInfo)
 registerNative(server,c,path,slots,calls,"file_search","Search one UTF-8 file using a Go regular expression.",fileSearch)
 registerNative(server,c,path,slots,calls,"file_grep","Search files recursively using a Go regular expression and optional glob filters.",fileGrep)
 registerNative(server,c,path,slots,calls,"file_glob","Find paths matching a glob pattern, including **.",fileGlob)
 registerNative(server,c,path,slots,calls,"directory_list","List directory entries with bounded recursion.",directoryList)
 registerNative(server,c,path,slots,calls,"batch","Run independent read-only native tool operations with bounded parallelism and output.",batch)
}
type BatchOperation struct{ID string `json:"id"`;Tool string `json:"tool"`;Arguments map[string]any `json:"arguments"`}
type BatchInput struct{Operations []BatchOperation `json:"operations"`;Parallel bool `json:"parallel,omitempty"`}
type BatchResult struct{ID string `json:"id"`;Result any `json:"result,omitempty"`;Error string `json:"error,omitempty"`}
type BatchOutput struct{Results []BatchResult `json:"results"`;Truncated bool `json:"truncated"`}
func dispatch[I,O any](ctx context.Context,c Config,args map[string]any,fn toolFunc[I,O])(any,error){
 b,e:=json.Marshal(args);if e!=nil{return nil,e};var in I
 d:=json.NewDecoder(bytes.NewReader(b));d.DisallowUnknownFields();if e=d.Decode(&in);e!=nil{return nil,e};return fn(ctx,c,in)
}
func readOperation(ctx context.Context,c Config,op BatchOperation)(any,error){
 if !c.enabled(op.Tool){return nil,errors.New("tool not enabled")}
 switch op.Tool{
 case "file_read":return dispatch(ctx,c,op.Arguments,fileRead)
 case "file_info":return dispatch(ctx,c,op.Arguments,fileInfo)
 case "file_search":return dispatch(ctx,c,op.Arguments,fileSearch)
 case "file_grep":return dispatch(ctx,c,op.Arguments,fileGrep)
 case "file_glob":return dispatch(ctx,c,op.Arguments,fileGlob)
 case "directory_list":return dispatch(ctx,c,op.Arguments,directoryList)
 default:return nil,errors.New("batch permits read-only operations only")
 }
}
type batchSlotsKey struct{}
type batchLoggerKey struct{}
func batch(ctx context.Context,c Config,in BatchInput)(BatchOutput,error){
 out:=BatchOutput{Results:[]BatchResult{}}
 if len(in.Operations)==0||len(in.Operations)>c.Batch.MaxOperations{return out,errors.New("invalid batch size")}
 ids:=map[string]bool{}
 for _,op:=range in.Operations{if op.ID==""||len(op.ID)>128||ids[op.ID]{return out,errors.New("batch IDs must be nonempty and unique")};ids[op.ID]=true}
 slots,_:=ctx.Value(batchSlotsKey{}).(chan struct{})
 if slots==nil{slots=make(chan struct{},maxInt(1,c.Batch.MaxParallel))}
 workers:=1;if in.Parallel{workers=min(c.Batch.MaxParallel,len(in.Operations),cap(slots))}
 // Process one bounded window at a time. Each operation holds a shared slot;
 // the coordinator itself never holds one, avoiding nested semaphore deadlocks.
 used:=64
 for base:=0;base<len(in.Operations);base+=workers{
  if e:=ctx.Err();e!=nil{return out,e}
  end:=min(base+workers,len(in.Operations))
  results:=make([]json.RawMessage,end-base)
  var wg sync.WaitGroup
  for i:=base;i<end;i++{wg.Add(1);go func(i int){defer wg.Done()
   op:=in.Operations[i]
   r:=BatchResult{ID:op.ID}
   if logger,ok:=ctx.Value(batchLoggerKey{}).(*callLogger);ok{finish:=logger.begin(op.Tool,op.Arguments);defer func(){finish(r.Error=="", " batch_id="+fmt.Sprintf("%q",op.ID),r)}()}
   select{case slots<-struct{}{}:
    defer func(){<-slots}()
    limited:=c
    limited.Filesystem.MaxReadBytes=min(c.Filesystem.MaxReadBytes,c.Batch.MaxOutputBytes)
    limited.Filesystem.MaxSearchResults=min(c.Filesystem.MaxSearchResults,maxInt(1,c.Batch.MaxOutputBytes/256))
    v,e:=readOperation(ctx,limited,op);r.Result=v;if e!=nil{r.Error=e.Error()}
   case <-ctx.Done():r.Error=ctx.Err().Error()
   }
   b,e:=json.Marshal(r)
   if e!=nil||len(b)>c.Batch.MaxOutputBytes{r.Error="operation output exceeds batch budget";b,_=json.Marshal(BatchResult{ID:op.ID,Error:r.Error})}
   results[i-base]=b
  }(i)}
  wg.Wait()
  for _,b:=range results{
   if used+len(b)+1>c.Batch.MaxOutputBytes{out.Truncated=true;return out,nil}
   var r BatchResult
   if e:=json.Unmarshal(b,&r);e!=nil{return out,fmt.Errorf("decode batch result: %w",e)}
   used+=len(b)+1;out.Results=append(out.Results,r)
  }
 }
 return out,nil
}
