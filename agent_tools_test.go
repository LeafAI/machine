package main

import (
 "context"
 "encoding/json"
 "strings"
 "time"
 "github.com/modelcontextprotocol/go-sdk/mcp"
 "os"
 "path/filepath"
 "testing"
)

func testNativeConfig(t *testing.T) Config {
 t.Helper()
 dir,e:=filepath.EvalSymlinks(t.TempDir());if e!=nil{t.Fatal(e)}
 f:=defaultFilesystem();f.AllowedRoots=[]string{dir};f.DefaultWorkdir=dir
 return Config{Filesystem:f,Batch:defaultBatch(),Tools:ToolConfig{Enabled:[]string{"shell","file_read","file_write","file_edit","file_info","file_search","file_grep","file_glob","directory_list","batch"}},TimeoutSeconds:5,MaxOutputBytes:1024,Shell:"/bin/sh"}
}
func TestNativeFiles(t *testing.T){
 c:=testNativeConfig(t);ctx:=context.Background()
 w,e:=fileWrite(ctx,c,WriteInput{Path:"main.go",Content:"first\nsecond\nthird\n"});if e!=nil{t.Fatal(e)}
 if _,e=fileWrite(ctx,c,WriteInput{Path:"main.go",Content:"overwrite"});e==nil{t.Fatal("implicit overwrite allowed")}
 rd,e:=fileRead(ctx,c,ReadInput{Path:"main.go",StartLine:2,EndLine:3});if e!=nil||rd.Content!="second\nthird\n"||rd.SHA256!=w.SHA256{t.Fatalf("%+v %v",rd,e)}
 if _,e=fileEdit(ctx,c,EditInput{Path:"main.go",OldString:"second",NewString:"changed",ExpectedSHA256:"stale"});e==nil{t.Fatal("stale hash accepted")}
 _,e=fileEdit(ctx,c,EditInput{Path:"main.go",OldString:"second",NewString:"changed",ExpectedSHA256:rd.SHA256});if e!=nil{t.Fatal(e)}
 rd,e=fileRead(ctx,c,ReadInput{Path:"main.go",StartLine:1,EndLine:3});if e!=nil||rd.Content!="first\nchanged\nthird\n"{t.Fatalf("%+v %v",rd,e)}
 if _,e=fileRead(ctx,c,ReadInput{Path:"../outside",StartLine:1,EndLine:1});e==nil{t.Fatal("root escape accepted")}
 outside:=filepath.Join(t.TempDir(),"outside");if e=os.WriteFile(outside,[]byte("outside"),0600);e!=nil{t.Fatal(e)}
 if e=os.Symlink(outside,filepath.Join(c.Filesystem.DefaultWorkdir,"link"));e!=nil{t.Fatal(e)}
 if _,e=fileRead(ctx,c,ReadInput{Path:"link",StartLine:1,EndLine:1});e==nil{t.Fatal("symlink accepted")}
}
func TestNativeSearchAndBatch(t *testing.T){
 c:=testNativeConfig(t);ctx:=context.Background()
 for p,s:=range map[string]string{"main.go":"needle\n","pkg/test.go":"needle\n","ignored.go":"needle\n",".gitignore":"ignored.go\n"}{
  if _,e:=fileWrite(ctx,c,WriteInput{Path:p,Content:s,CreateParents:true});e!=nil{t.Fatal(e)}
 }
 g,e:=fileGlob(ctx,c,GlobInput{Path:".",Pattern:"**/*.go"});if e!=nil||len(g.Paths)!=2{t.Fatalf("%+v %v",g,e)}
 m,e:=fileGrep(ctx,c,GrepInput{Paths:[]string{"."},Pattern:"needle",Include:[]string{"**/*.go"}});if e!=nil||len(m.Matches)!=2{t.Fatalf("%+v %v",m,e)}
 b,e:=batch(ctx,c,BatchInput{Parallel:true,Operations:[]BatchOperation{{ID:"read",Tool:"file_read",Arguments:map[string]any{"path":"main.go","start_line":1,"end_line":1}},{ID:"write",Tool:"file_write",Arguments:map[string]any{"path":"forbidden","content":"x"}}}})
 if e!=nil||len(b.Results)!=2||b.Results[0].Error!=""||b.Results[1].Error==""{t.Fatalf("%+v %v",b,e)}
}
func TestShellOutputModes(t *testing.T){
 c:=testNativeConfig(t)
 for _,v:=range []struct{mode,want string}{{"head","abcd"},{"tail","ghij"},{"head_tail","abij"}}{
  o:=run(context.Background(),c,Input{Command:"printf abcdefghij",MaxOutputBytes:4,OutputMode:v.mode})
  if o.Stdout!=v.want||!o.Truncated||o.ExitCode!=0{t.Fatalf("%s: %+v",v.mode,o)}
 }
}

func TestBatchSharedSlots(t *testing.T){
 c:=testNativeConfig(t);slots:=make(chan struct{},1);slots<-struct{}{}
 ctx,cancel:=context.WithTimeout(context.Background(),50*time.Millisecond);defer cancel()
 ctx=context.WithValue(ctx,batchSlotsKey{},slots)
 out,_:=batch(ctx,c,BatchInput{Parallel:true,Operations:[]BatchOperation{{ID:"a",Tool:"file_info",Arguments:map[string]any{"paths":[]string{"."}}}}})
 if len(out.Results)!=1||out.Results[0].Error==""{t.Fatalf("expected saturated slot cancellation: %+v",out)}
 <-slots
 ctx=context.WithValue(context.Background(),batchSlotsKey{},slots)
 out,e:=batch(ctx,c,BatchInput{Parallel:true,Operations:[]BatchOperation{{ID:"a",Tool:"file_info",Arguments:map[string]any{"paths":[]string{"."}}},{ID:"b",Tool:"file_info",Arguments:map[string]any{"paths":[]string{"."}}}}})
 if e!=nil||len(out.Results)!=2||out.Results[0].Error!=""||out.Results[1].Error!=""{t.Fatalf("%+v %v",out,e)}
}
func TestBatchBudget(t *testing.T){
 c:=testNativeConfig(t);c.Batch.MaxOutputBytes=1024;ctx:=context.Background()
 if _,e:=fileWrite(ctx,c,WriteInput{Path:"large",Content:strings.Repeat("x",900)});e!=nil{t.Fatal(e)}
 in:=BatchInput{Parallel:true}
 for _,id:=range []string{"a","b","c","d"}{in.Operations=append(in.Operations,BatchOperation{ID:id,Tool:"file_read",Arguments:map[string]any{"path":"large","start_line":1,"end_line":1}})}
 out,e:=batch(ctx,c,in);if e!=nil{t.Fatal(e)}
 b,e:=json.Marshal(out);if e!=nil||len(b)>c.Batch.MaxOutputBytes{t.Fatalf("budget exceeded: %d %v",len(b),e)}
}
func TestExpandingEditLimit(t *testing.T){
 c:=testNativeConfig(t);c.Filesystem.MaxWriteBytes=1024
 ctx:=context.Background()
 if _,e:=fileWrite(ctx,c,WriteInput{Path:"a",Content:strings.Repeat("x",100)});e!=nil{t.Fatal(e)}
 if _,e:=fileEdit(ctx,c,EditInput{Path:"a",OldString:"x",NewString:strings.Repeat("y",100),ReplaceAll:true});e==nil{t.Fatal("oversized expansion accepted")}
}
func TestAllToolsMCP(t *testing.T){
 c:=testNativeConfig(t);p:=filepath.Join(c.Filesystem.DefaultWorkdir,"config.yaml")
 cfg:="auth_required: false\nmax_concurrent: 1\ntools:\n  enabled: ["+strings.Join(c.Tools.Enabled,", ")+"]\nfilesystem:\n  default_workdir: '"+c.Filesystem.DefaultWorkdir+"'\n  allowed_roots: ['"+c.Filesystem.DefaultWorkdir+"']\n"
 if e:=os.WriteFile(p,[]byte(cfg),0600);e!=nil{t.Fatal(e)}
 c,e:=loadConfig(p);if e!=nil{t.Fatal(e)}
 srv:=newMachineServer(c,p);st,ct:=mcp.NewInMemoryTransports()
 ctx,cancel:=context.WithTimeout(context.Background(),20*time.Second);defer cancel()
 go func(){_=srv.Run(ctx,st)}()
 client:=mcp.NewClient(&mcp.Implementation{Name:"test",Version:"1"},nil)
 session,e:=client.Connect(ctx,ct,nil);if e!=nil{t.Fatal(e)};defer session.Close()
 list,e:=session.ListTools(ctx,nil);if e!=nil||len(list.Tools)!=10{t.Fatalf("tool discovery: %v",e)}
 for _,v:=range []struct{name string;args map[string]any}{
 {"shell",map[string]any{"command":"printf ok"}},
 {"file_write",map[string]any{"path":"a.txt","content":"needle"}},
 {"file_read",map[string]any{"path":"a.txt","start_line":1,"end_line":1}},
 {"file_edit",map[string]any{"path":"a.txt","old_string":"needle","new_string":"changed"}},
 {"file_info",map[string]any{"paths":[]string{"a.txt"}}},
 {"file_search",map[string]any{"path":"a.txt","pattern":"changed"}},
 {"file_grep",map[string]any{"paths":[]string{"."},"pattern":"changed"}},
 {"file_glob",map[string]any{"path":".","pattern":"**/*.txt"}},
 {"directory_list",map[string]any{"path":"."}},
 {"batch",map[string]any{"parallel":true,"operations":[]map[string]any{{"id":"a","tool":"file_info","arguments":map[string]any{"paths":[]string{"a.txt"}}},{"id":"b","tool":"file_read","arguments":map[string]any{"path":"a.txt","start_line":1,"end_line":1}}}}},
 }{
  res,e:=session.CallTool(ctx,&mcp.CallToolParams{Name:v.name,Arguments:v.args})
  if e!=nil||res==nil||res.IsError||res.StructuredContent==nil{t.Fatalf("%s: %v",v.name,e)}
  b,e:=json.Marshal(res.StructuredContent);if e!=nil{t.Fatal(e)}
  var obj map[string]any;if e=json.Unmarshal(b,&obj);e!=nil{t.Fatal(e)}
  if obj["error"]!=nil&&obj["error"]!=""{t.Fatalf("%s: %s",v.name,b)}
 }
}
