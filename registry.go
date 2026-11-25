package main

import (
 "context"
 "encoding/json"
 "errors"
 "fmt"
 "sync"
 "time"

 "github.com/modelcontextprotocol/go-sdk/mcp"
)

type ToolResponse[T any] struct {
 Result T `json:"result"`
 Error string `json:"error,omitempty"`
}
type toolFunc[I,O any] func(context.Context,Config,I)(O,error)
func registerNative[I,O any](server *mcp.Server,start Config,path string,slots chan struct{},name,description string,fn toolFunc[I,O]){
 if !start.enabled(name){return}
 mcp.AddTool(server,&mcp.Tool{Name:name,Description:description},func(ctx context.Context,_ *mcp.CallToolRequest,in I)(*mcp.CallToolResult,ToolResponse[O],error){
  var out ToolResponse[O]
  select{case slots<-struct{}{}:defer func(){<-slots}();case <-ctx.Done():return nil,out,ctx.Err();default:out.Error="tool concurrency limit reached";return &mcp.CallToolResult{},out,nil}
  c,e:=loadConfig(path);if e!=nil{out.Error="invalid configuration";return &mcp.CallToolResult{},out,nil}
  // Registration is fixed until restart; enforce startup allowlist in batches.
  c.Tools=start.Tools
  ctx,cancel:=context.WithTimeout(ctx,time.Duration(c.TimeoutSeconds)*time.Second);defer cancel()
  out.Result,e=fn(ctx,c,in);if e!=nil{out.Error=e.Error()}
  return &mcp.CallToolResult{},out,nil
 })
}
func registerNativeTools(server *mcp.Server,c Config,path string,slots chan struct{}){
 registerNative(server,c,path,slots,"file_read","Read an explicit inclusive line range of a UTF-8 file; returns content and SHA-256.",fileRead)
 registerNative(server,c,path,slots,"file_write","Create a UTF-8 file, or explicitly overwrite it. Supports conditional SHA-256.",fileWrite)
 registerNative(server,c,path,slots,"file_edit","Replace an exact unique string or inclusive line range; optional expected SHA-256.",fileEdit)
 registerNative(server,c,path,slots,"file_info","Get metadata for multiple paths.",fileInfo)
 registerNative(server,c,path,slots,"file_search","Search one UTF-8 file using a Go regular expression.",fileSearch)
 registerNative(server,c,path,slots,"file_grep","Search files recursively using a Go regular expression and optional glob filters.",fileGrep)
 registerNative(server,c,path,slots,"file_glob","Find paths matching a glob pattern, including **.",fileGlob)
 registerNative(server,c,path,slots,"directory_list","List directory entries with bounded recursion.",directoryList)
 registerNative(server,c,path,slots,"batch","Run independent read-only native tool operations with bounded parallelism and output.",batch)
}
type BatchOperation struct{ID string `json:"id"`;Tool string `json:"tool"`;Arguments map[string]any `json:"arguments"`}
type BatchInput struct{Operations []BatchOperation `json:"operations"`;Parallel bool `json:"parallel,omitempty"`}
type BatchResult struct{ID string `json:"id"`;Result any `json:"result,omitempty"`;Error string `json:"error,omitempty"`}
type BatchOutput struct{Results []BatchResult `json:"results"`;Truncated bool `json:"truncated"`}
func dispatch[I,O any](ctx context.Context,c Config,args map[string]any,fn toolFunc[I,O])(any,error){
 b,e:=json.Marshal(args);if e!=nil{return nil,e};var in I
 if e=json.Unmarshal(b,&in);e!=nil{return nil,e};return fn(ctx,c,in)
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
func batch(ctx context.Context,c Config,in BatchInput)(BatchOutput,error){
 out:=BatchOutput{Results:[]BatchResult{}}
 if len(in.Operations)==0||len(in.Operations)>c.Batch.MaxOperations{return out,errors.New("invalid batch size")}
 ids:=map[string]bool{}
 for _,op:=range in.Operations{if op.ID==""||len(op.ID)>128||ids[op.ID]{return out,errors.New("batch IDs must be nonempty and unique")};ids[op.ID]=true}
 workers:=1;if in.Parallel{workers=min(c.Batch.MaxParallel,len(in.Operations))}
 results:=make([]BatchResult,len(in.Operations));var wg sync.WaitGroup
 jobs:=make(chan int)
 for i:=0;i<workers;i++{wg.Add(1);go func(){defer wg.Done();for n:=range jobs{op:=in.Operations[n];v,e:=readOperation(ctx,c,op);r:=BatchResult{ID:op.ID,Result:v};if e!=nil{r.Error=e.Error()};results[n]=r}}()}
 for i:=range in.Operations{jobs<-i};close(jobs);wg.Wait()
 used:=64
 for _,r:=range results{b,e:=json.Marshal(r);if e!=nil{return out,fmt.Errorf("encode batch result: %w",e)};if used+len(b)+1>c.Batch.MaxOutputBytes{out.Truncated=true;break};used+=len(b)+1;out.Results=append(out.Results,r)}
 return out,nil
}
