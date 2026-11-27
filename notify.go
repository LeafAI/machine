package main

import (
 "context"
 "errors"
 "fmt"
 "log/slog"
 "os"
 "os/exec"
 "runtime"
 "strconv"
 "strings"
 "sync"
 "time"
 "unicode/utf8"
)

type NotifyConfig struct {
 DefaultTitle string `yaml:"default_title"`
 DefaultMode string `yaml:"default_mode"`
 DefaultTimeoutSeconds int `yaml:"default_timeout_seconds"`
 DefaultSound string `yaml:"default_sound"`
 MaxMessageBytes int `yaml:"max_message_bytes"`
 MaxPendingDialogs int `yaml:"max_pending_dialogs"`
}
func defaultNotify() NotifyConfig {
 return NotifyConfig{DefaultTitle:"Machine",DefaultMode:"dialog",MaxMessageBytes:4096,MaxPendingDialogs:4}
}
func validNotifyText(s string,limit int)bool{
 return len(s)<=limit&&utf8.ValidString(s)&&!strings.ContainsRune(s,0)
}
func(c NotifyConfig) validate()error{
 if !validNotifyText(c.DefaultTitle,256)||strings.TrimSpace(c.DefaultTitle)==""{return errors.New("invalid notify.default_title")}
 if c.DefaultMode!="dialog"&&c.DefaultMode!="notification"{return errors.New("invalid notify.default_mode")}
 if c.DefaultTimeoutSeconds<0||c.DefaultTimeoutSeconds>86400{return errors.New("invalid notify.default_timeout_seconds")}
 if !validNotifyText(c.DefaultSound,128){return errors.New("invalid notify.default_sound")}
 if c.MaxMessageBytes<1||c.MaxMessageBytes>16384||c.MaxPendingDialogs<1||c.MaxPendingDialogs>32{return errors.New("invalid notify resource limits")}
 return nil
}
type NotifyInput struct{
 Message string `json:"message" jsonschema:"Message to display on the local Mac"`
 Title string `json:"title,omitempty"`
 Mode string `json:"mode,omitempty" jsonschema:"dialog or notification"`
 TimeoutSeconds *int `json:"timeout_seconds,omitempty" jsonschema:"Dialog auto-dismiss seconds; zero means no automatic dismissal"`
 Sound *string `json:"sound,omitempty" jsonschema:"Notification sound name; empty disables the configured sound"`
}
type NotifyOutput struct{
 Status string `json:"status"`
 Mode string `json:"mode"`
}
// All user content is passed as argv, never interpolated into executable code.
const notifyScript = `on run argv
 set msg to item 1 of argv
 set heading to item 2 of argv
 set presentation to item 3 of argv
 set secondsToWait to (item 4 of argv) as integer
 set soundName to item 5 of argv
 if presentation is "dialog" then
  if secondsToWait > 0 then
   display dialog msg with title heading buttons {"OK"} default button "OK" giving up after secondsToWait
  else
   display dialog msg with title heading buttons {"OK"} default button "OK"
  end if
 else
  if soundName is "" then
   display notification msg with title heading
  else
   display notification msg with title heading sound name soundName
  end if
 end if
end run`
func notifyArgs(c NotifyConfig,in NotifyInput)([]string,string,int,error){
 if e:=c.validate();e!=nil{return nil,"",0,e}
 if !validNotifyText(in.Message,c.MaxMessageBytes)||strings.TrimSpace(in.Message)==""{return nil,"",0,errors.New("invalid notification message")}
 title,mode,sound,seconds:=in.Title,in.Mode,c.DefaultSound,c.DefaultTimeoutSeconds
 if title==""{title=c.DefaultTitle}
 if mode==""{mode=c.DefaultMode}
 if in.Sound!=nil{sound=*in.Sound}
 if in.TimeoutSeconds!=nil{seconds=*in.TimeoutSeconds}
 if !validNotifyText(title,256)||strings.TrimSpace(title)==""||!validNotifyText(sound,128)||seconds<0||seconds>86400{return nil,"",0,errors.New("invalid notification arguments")}
 if mode!="dialog"&&mode!="notification"{return nil,"",0,errors.New("invalid notification mode")}
 if mode=="notification"&&in.TimeoutSeconds!=nil&&seconds!=0{return nil,"",0,errors.New("timeout_seconds applies only to dialogs")}
 if mode=="dialog"&&in.Sound!=nil&&sound!=""{return nil,"",0,errors.New("sound applies only to notifications")}
 if mode=="notification"{seconds=0}else{sound=""}
 return []string{"-e",notifyScript,"--",in.Message,title,mode,strconv.Itoa(seconds),sound},mode,seconds,nil
}

// Per-server bounds persist across request and tunnel cancellation. Start
// synchronously, then reap asynchronously; no MCP invocation waits for OK.
type notifier struct{
 mu sync.Mutex
 pending int
 logger *callLogger
 platform string
 command func(context.Context,[]string)*exec.Cmd
}
func newNotifier(logger *callLogger)*notifier{
 return &notifier{logger:logger,platform:runtime.GOOS,command:func(ctx context.Context,args []string)*exec.Cmd{
  cmd:=exec.CommandContext(ctx,"/usr/bin/osascript",args...)
  cmd.Dir="/"
  cmd.Env=shellEnvironment(os.Environ())
  return cmd
 }}
}
func(n *notifier) release(){n.mu.Lock();n.pending--;n.mu.Unlock()}
func(n *notifier) submit(ctx context.Context,c Config,in NotifyInput)(NotifyOutput,error){
 if n.platform!="darwin"{return NotifyOutput{},errors.New("notify requires macOS")}
 args,mode,seconds,e:=notifyArgs(c.Notify,in);if e!=nil{return NotifyOutput{},e}
 if e=ctx.Err();e!=nil{return NotifyOutput{},e}
 n.mu.Lock()
 if n.pending>=c.Notify.MaxPendingDialogs{n.mu.Unlock();return NotifyOutput{},errors.New("notification pending limit reached")}
 n.pending++;n.mu.Unlock()
 // Notification commands have a finite watchdog. Dialogs with zero timeout
 // intentionally remain until acknowledged. This context is not the RPC's.
 lifetime:=context.Background()
 cancel:=func(){}
 if mode=="notification"{lifetime,cancel=context.WithTimeout(lifetime,30*time.Second)}else if seconds>0{lifetime,cancel=context.WithTimeout(lifetime,time.Duration(seconds+15)*time.Second)}
 cmd:=n.command(lifetime,args)
 if e=ctx.Err();e!=nil{cancel();n.release();return NotifyOutput{},e}
 if e=cmd.Start();e!=nil{cancel();n.release();return NotifyOutput{},fmt.Errorf("cannot start notification: %w",e)}
 go func(){
  defer n.release();defer cancel()
  e:=cmd.Wait()
  if n.logger!=nil{
   if e!=nil{n.logger.line(slog.LevelWarn,"notify process failed mode=%s pid=%d",mode,cmd.Process.Pid)}else{n.logger.line(slog.LevelDebug,"notify process completed mode=%s pid=%d",mode,cmd.Process.Pid)}
  }
 }()
 // Submitted means osascript started, not that the GUI displayed the message.
 return NotifyOutput{Status:"submitted",Mode:mode},nil
}
