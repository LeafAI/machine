package main

import (
 "context"
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
