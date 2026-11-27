package main

import (
 "context"
 "os/exec"
 "path/filepath"
 "os"
 "reflect"
 "testing"
 "time"

 "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestNotifyArguments(t *testing.T){
 c:=defaultNotify()
 msg:="Quotes \" and newline\n; do shell script \"unexpected\""
 args,mode,seconds,e:=notifyArgs(c,NotifyInput{Message:msg})
 if e!=nil||mode!="dialog"||seconds!=0{t.Fatalf("default dialog: %v",e)}
 if !reflect.DeepEqual(args,[]string{"-e",notifyScript,"--",msg,"Machine","dialog","0",""}){t.Fatal("message not passed verbatim as argv")}
 c.DefaultSound="Glass";c.DefaultTimeoutSeconds=20
 sound:="";zero:=0
 args,mode,seconds,e=notifyArgs(c,NotifyInput{Message:"done",Mode:"notification",Sound:&sound})
 if e!=nil||mode!="notification"||seconds!=0||args[7]!=""{t.Fatalf("notification override: %v",e)}
 _,_,seconds,e=notifyArgs(c,NotifyInput{Message:"done",TimeoutSeconds:&zero})
 if e!=nil||seconds!=0{t.Fatal("explicit zero timeout did not override default")}
 neg:=-1;one:=1;glass:="Glass"
 for _,in:=range []NotifyInput{{},{Message:" "},{Message:"a\x00b"},{Message:"done",Mode:"bad"},{Message:"done",TimeoutSeconds:&neg},{Message:"done",Mode:"notification",TimeoutSeconds:&one},{Message:"done",Sound:&glass}}{
  if _,_,_,e=notifyArgs(c,in);e==nil{t.Fatalf("invalid input accepted: %+v",in)}
 }
 c.MaxMessageBytes=2
 if _,_,_,e=notifyArgs(c,NotifyInput{Message:"日本語"});e==nil{t.Fatal("byte limit not enforced")}
}
func TestNotifyDetachedAndBounded(t *testing.T){
 c:=Config{Notify:defaultNotify()};c.Notify.MaxPendingDialogs=1
 n:=newNotifier(nil);n.platform="darwin"
 gate:=filepath.Join(t.TempDir(),"release")
 n.command=func(ctx context.Context,args []string)*exec.Cmd{return exec.CommandContext(ctx,"/bin/sh","-c",`while [ ! -f "$1" ]; do sleep 0.02; done`,"sh",gate)}
 ctx,cancel:=context.WithCancel(context.Background());defer cancel()
 out,e:=n.submit(ctx,c,NotifyInput{Message:"test"})
 if e!=nil||out.Status!="submitted"{t.Fatalf("submit: %v",e)}
 cancel()
 if _,e=n.submit(context.Background(),c,NotifyInput{Message:"second"});e==nil{t.Fatal("pending limit not enforced")}
 if e=os.WriteFile(gate,[]byte("ok"),0600);e!=nil{t.Fatal(e)}
 deadline:=time.Now().Add(3*time.Second)
 for {
  n.mu.Lock();pending:=n.pending;n.mu.Unlock()
  if pending==0{break}
  if time.Now().After(deadline){t.Fatal("child was not reaped")}
  time.Sleep(10*time.Millisecond)
 }
 n.command=func(ctx context.Context,args []string)*exec.Cmd{return exec.CommandContext(ctx,"/nonexistent-machine-notify-test")}
 if _,e=n.submit(context.Background(),c,NotifyInput{Message:"test"});e==nil{t.Fatal("start failure accepted")}
 n.mu.Lock();pending:=n.pending;n.mu.Unlock()
 if pending!=0{t.Fatal("start failure leaked slot")}
 n.platform="linux"
 if _,e=n.submit(context.Background(),c,NotifyInput{Message:"test"});e==nil{t.Fatal("unsupported platform accepted")}
}
func TestNotifyOptInAndConfig(t *testing.T){
 p:=filepath.Join(t.TempDir(),"machine.yaml")
 if e:=os.WriteFile(p,[]byte("auth_required: false\ntools:\n  enabled: [notify]\nnotify:\n  default_title: Test\n  max_pending_dialogs: 2\n"),0600);e!=nil{t.Fatal(e)}
 c,e:=loadConfig(p);if e!=nil{t.Fatal(e)}
 if c.Notify.DefaultTitle!="Test"||c.Notify.DefaultMode!="dialog"||c.Notify.MaxPendingDialogs!=2{t.Fatal("configuration mismatch")}
 ctx,cancel:=context.WithTimeout(context.Background(),5*time.Second);defer cancel()
 st,ct:=mcp.NewInMemoryTransports()
 server:=newMachineServer(c,p)
 go func(){_=server.Run(ctx,st)}()
 client:=mcp.NewClient(&mcp.Implementation{Name:"test",Version:"1"},nil)
 session,e:=client.Connect(ctx,ct,nil);if e!=nil{t.Fatal(e)};defer session.Close()
 list,e:=session.ListTools(ctx,nil);if e!=nil||len(list.Tools)!=1||list.Tools[0].Name!="notify"{t.Fatalf("notify discovery: %v",e)}
 // Invalid message exercises the actual MCP handler without opening a popup.
 res,e:=session.CallTool(ctx,&mcp.CallToolParams{Name:"notify",Arguments:map[string]any{"message":" "}})
 if e!=nil||res==nil||res.StructuredContent==nil{t.Fatalf("notify response: %v",e)}
 obj,ok:=res.StructuredContent.(map[string]any);if !ok||obj["tool_call_id"]==nil||obj["error"]==nil{t.Fatalf("missing error/correlation: %+v",res.StructuredContent)}
 if _,e:=readOperation(ctx,c,BatchOperation{Tool:"notify",Arguments:map[string]any{"message":"test"}});e==nil{t.Fatal("batch accepted notification")}
 if e=os.WriteFile(p,[]byte("auth_required: false\nnotify:\n  max_pending_dialogs: 0\n"),0600);e!=nil{t.Fatal(e)}
 if _,e=loadConfig(p);e==nil{t.Fatal("invalid notify config accepted")}
}
