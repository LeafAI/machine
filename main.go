package main

import (
 "bytes"
 "context"
 "crypto/sha256"
 "crypto/subtle"
 "errors"
 "fmt"
 "io"
 "strconv"
 "unicode"
 "log"
 "log/slog"
 "net"
 "net/http"
 "os"
 "os/exec"
 "os/signal"
 "path/filepath"
 "strings"
 "syscall"
 "time"

 "github.com/modelcontextprotocol/go-sdk/mcp"
 "gopkg.in/yaml.v3"
)

type Config struct {
 Listen string `yaml:"listen"`
 APIKey string `yaml:"api_key"`
 AuthRequired bool `yaml:"auth_required"`
 Logging LoggingConfig `yaml:"logging"`
 Tools ToolConfig `yaml:"tools"`
 Filesystem FilesystemConfig `yaml:"filesystem"`
 Batch BatchConfig `yaml:"batch"`
 Tunnel TunnelConfig `yaml:"tunnel"`
 Shell string `yaml:"shell"`
 AdditionalPaths []string `yaml:"additional_paths"`
 TimeoutSeconds int `yaml:"timeout_seconds"`
 MaxOutputBytes int `yaml:"max_output_bytes"`
 MaxConcurrent int `yaml:"max_concurrent"`
}

func loadConfig(path string) (Config,error) {return loadConfigWithWarnings(path,false)}

func loadConfigWithWarnings(path string,warnUnknown bool) (Config,error) {
 c:=Config{Logging:defaultLogging(),Tools:ToolConfig{Enabled:[]string{"shell"}},Filesystem:defaultFilesystem(),Batch:defaultBatch(),AuthRequired:true,Listen:"127.0.0.1:6666",Shell:"/bin/sh",TimeoutSeconds:300,MaxOutputBytes:1048576,MaxConcurrent:4}
 f,e:=os.OpenFile(path,os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW,0); if e!=nil{return c,e}; defer f.Close()
 st,e:=f.Stat(); if e!=nil{return c,e}
 if !st.Mode().IsRegular() || st.Mode().Perm()&0077!=0 || st.Size()>1<<20{return c,errors.New("configuration must be a regular private file (chmod 600)")}
 if owner,ok:=st.Sys().(*syscall.Stat_t);!ok||owner.Uid!=uint32(os.Geteuid()){return c,errors.New("configuration must be owned by the server user")}
 data,e:=io.ReadAll(io.LimitReader(f,(1<<20)+1));if e!=nil{return c,e}
 if len(data)>1<<20{return c,errors.New("configuration exceeds 1 MiB")}
 d:=yaml.NewDecoder(bytes.NewReader(data))
 if e=d.Decode(&c);e!=nil{return c,errors.New("invalid configuration YAML or known setting type")}
 var extra any
 if e=d.Decode(&extra);e!=io.EOF{return c,errors.New("configuration must contain exactly one YAML document")}
 // Forward-compatible decoding ignores unknown keys, including nested keys.
 // A strict diagnostic pass at startup warns without exposing YAML values.
 if warnUnknown{var probe Config;strict:=yaml.NewDecoder(bytes.NewReader(data));strict.KnownFields(true);if strict.Decode(&probe)!=nil{log.Print("WARNING: configuration contains unknown keys; ignored by this machine version")}}
 if c.AuthRequired && (len(c.APIKey)<32 || strings.IndexFunc(c.APIKey,func(r rune)bool{return unicode.IsSpace(r)||unicode.IsControl(r)})>=0){return c,errors.New("api_key must contain at least 32 non-whitespace characters")}
 host,port,e:=net.SplitHostPort(c.Listen);if e!=nil{return c,errors.New("invalid listen address")}
 n,e:=strconv.Atoi(port);if e!=nil||n<1||n>65535{return c,errors.New("invalid listen port")}
 ip:=net.ParseIP(host);if ip==nil || !ip.IsLoopback(){return c,errors.New("listen must be a loopback IP; use a secure tunnel")}
 if !filepath.IsAbs(c.Shell)||c.TimeoutSeconds<1||c.TimeoutSeconds>86400||c.MaxOutputBytes<1||c.MaxOutputBytes>16777216||c.MaxConcurrent<1||c.MaxConcurrent>128{return c,errors.New("invalid shell or resource limits")}
 for i,p:=range c.AdditionalPaths {
  p=os.ExpandEnv(p)
  if strings.HasPrefix(p,"~/"){h,e:=os.UserHomeDir();if e!=nil{return c,e};p=filepath.Join(h,p[2:])}
  if !filepath.IsAbs(p)||strings.ContainsRune(p,os.PathListSeparator){return c,errors.New("additional_paths must be absolute directories")}
  c.AdditionalPaths[i]=p
 }
 if e=c.Logging.validate();e!=nil{return c,e}
 if e=c.validateTools();e!=nil{return c,e}
 return c,nil
}

type Input struct {
 Command string `json:"command" jsonschema:"Shell command to execute"`
 Workdir string `json:"workdir,omitempty" jsonschema:"Optional absolute working directory"`
 TimeoutSeconds int `json:"timeout_seconds,omitempty"`
 MaxOutputBytes int `json:"max_output_bytes,omitempty"`
 OutputMode string `json:"output_mode,omitempty"`
}
type Output struct {
 Stdout string `json:"stdout"`
 Stderr string `json:"stderr"`
 ExitCode int `json:"exit_code"`
 TimedOut bool `json:"timed_out"`
 Truncated bool `json:"truncated"`
 Error string `json:"error,omitempty"`
 DurationMS int64 `json:"duration_ms"`
 Signal string `json:"signal,omitempty"`
}
type capped struct{b bytes.Buffer;limit int;truncated bool; mode string; tail []byte}
func(w *capped)Write(p []byte)(int,error){
 n:=len(p)
 if w.mode=="tail"||w.mode=="head_tail"{
  head:=0;if w.mode=="head_tail"{head=w.limit/2};tailLimit:=w.limit-head
  if remain:=head-w.b.Len();remain>0{take:=min(remain,len(p));_,_=w.b.Write(p[:take]);p=p[take:]}
  if len(p)>=tailLimit{if len(w.tail)+len(p)>tailLimit{w.truncated=true};w.tail=append(w.tail[:0],p[len(p)-tailLimit:]...)}else{if excess:=len(w.tail)+len(p)-tailLimit;excess>0{w.truncated=true;copy(w.tail,w.tail[excess:]);w.tail=w.tail[:len(w.tail)-excess]};w.tail=append(w.tail,p...)}
  return n,nil
 }
 remaining:=w.limit-w.b.Len()
 if n>remaining{w.truncated=true;p=p[:remaining]}
 _,_=w.b.Write(p);return n,nil
}
func(w *capped) String()string{return w.b.String()+string(w.tail)}
func run(ctx context.Context,c Config,in Input)(out Output){
 start:=time.Now();defer func(){out.DurationMS=time.Since(start).Milliseconds()}()
 out=Output{ExitCode:-1}
 if in.TimeoutSeconds<0||in.TimeoutSeconds>c.TimeoutSeconds||in.MaxOutputBytes<0||in.MaxOutputBytes>c.MaxOutputBytes{out.Error="requested limits exceed configured maximums";return out}
 if in.TimeoutSeconds>0{c.TimeoutSeconds=in.TimeoutSeconds};if in.MaxOutputBytes>0{c.MaxOutputBytes=in.MaxOutputBytes}
 if in.OutputMode!=""&&in.OutputMode!="head"&&in.OutputMode!="tail"&&in.OutputMode!="head_tail"{out.Error="invalid output_mode";return out}
 if strings.TrimSpace(in.Command)==""{out.Error="command is empty";return out}
 if in.Workdir!=""&&!filepath.IsAbs(in.Workdir){out.Error="workdir must be absolute";return out}
 ctx,cancel:=context.WithTimeout(ctx,time.Duration(c.TimeoutSeconds)*time.Second);defer cancel()
 cmd:=exec.CommandContext(ctx,c.Shell,"-c",in.Command);cmd.Dir=in.Workdir
 paths:=append([]string{},c.AdditionalPaths...)
 if p:=os.Getenv("PATH");p!=""{paths=append(paths,p)}
 cmd.Env=append(shellEnvironment(cmd.Environ()),"PATH="+strings.Join(paths,string(os.PathListSeparator)))
 cmd.SysProcAttr=&syscall.SysProcAttr{Setpgid:true}
 cmd.Cancel=func()error{e:=syscall.Kill(-cmd.Process.Pid,syscall.SIGKILL);if errors.Is(e,syscall.ESRCH){return os.ErrProcessDone};return e}
 cmd.WaitDelay=2*time.Second
 stdout,stderr:=&capped{limit:c.MaxOutputBytes,mode:in.OutputMode},&capped{limit:c.MaxOutputBytes,mode:in.OutputMode}
 cmd.Stdout=stdout;cmd.Stderr=stderr
 e:=cmd.Run()
 out.Stdout=stdout.String();out.Stderr=stderr.String();out.Truncated=stdout.truncated||stderr.truncated
 out.TimedOut=errors.Is(ctx.Err(),context.DeadlineExceeded)
 if cmd.ProcessState!=nil{out.ExitCode=cmd.ProcessState.ExitCode();if status,ok:=cmd.ProcessState.Sys().(syscall.WaitStatus);ok&&status.Signaled(){out.Signal=status.Signal().String()}}
 if e!=nil{out.Error=e.Error()};if ctx.Err()!=nil{out.Error=ctx.Err().Error()}
 return out
}
func protect(key string,next http.Handler)http.Handler{
 return protectWithAuth(true,key,next)
}
func protectWithAuth(required bool,key string,next http.Handler)http.Handler{
 expected:=sha256.Sum256([]byte("Bearer "+key))
 return http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  if len(r.Header.Values("Origin"))!=0{http.Error(w,"origin forbidden",http.StatusForbidden);return}
  {
   host,_,err:=net.SplitHostPort(r.RemoteAddr);ip:=net.ParseIP(host)
   if err!=nil||ip==nil||!ip.IsLoopback(){http.Error(w,"loopback clients only",http.StatusForbidden);return}
   host=r.Host
   if h,_,err:=net.SplitHostPort(host);err==nil{host=h}
   ip=net.ParseIP(strings.Trim(host,"[]"))
   if !strings.EqualFold(host,"localhost")&&(ip==nil||!ip.IsLoopback()){http.Error(w,"invalid host",http.StatusForbidden);return}
  }
  actual:=sha256.Sum256([]byte(r.Header.Get("Authorization")))
  if required && (len(r.Header.Values("Authorization"))!=1||subtle.ConstantTimeCompare(expected[:],actual[:])!=1){
   w.Header().Set("WWW-Authenticate",`Bearer realm="machine"`);http.Error(w,"unauthorized",http.StatusUnauthorized);return
  }
  r.Body=http.MaxBytesReader(w,r.Body,1<<20);w.Header().Set("Cache-Control","no-store");next.ServeHTTP(w,r)
 })
}
func shellToolResult(ctx context.Context,c Config,in Input)(*mcp.CallToolResult,Output,error){
 out:=run(ctx,c,in)
 // A completed shell invocation is a valid MCP result even if the command fails.
 // Keep exit status, stderr and timeout information available to the caller.
 return &mcp.CallToolResult{},out,nil
}
// Read a fresh validated configuration for each invocation. No shared mutable config.
func shellToolFromConfig(ctx context.Context,path string,in Input)(*mcp.CallToolResult,Output,error){
 c,err:=loadConfig(path)
 if err!=nil{return &mcp.CallToolResult{},Output{ExitCode:-1,Error:"cannot load shell configuration: check YAML syntax, settings and file permissions"},nil}
 return shellToolResult(ctx,c,in)
}
func newMachineServer(c Config,configPath string)*mcp.Server{
 server:=mcp.NewServer(&mcp.Implementation{Name:"machine",Version:"0.1.0"},nil)
 slots:=make(chan struct{},c.MaxConcurrent)
 calls:=newServerCallLogger(c)
 calls.line(slog.LevelInfo,"logging args=%t output=%t arg_limit=%d output_limit=%d",calls.cfg.LogArguments,calls.cfg.LogOutput,calls.cfg.MaxArgumentBytes,calls.cfg.MaxOutputBytes)
 if c.enabled("shell"){mcp.AddTool(server,&mcp.Tool{Name:"shell",Description:"Execute an arbitrary non-interactive shell command with the server user permissions. May change files and system state."},func(ctx context.Context,_ *mcp.CallToolRequest,in Input)(result *mcp.CallToolResult,out Output,err error){
  finish:=calls.begin("shell",in);defer func(){finish(err==nil&&out.Error==""&&out.ExitCode==0,fmt.Sprintf(" exit=%d stdout_bytes=%d stderr_bytes=%d timeout=%t truncated=%t",out.ExitCode,len(out.Stdout),len(out.Stderr),out.TimedOut,out.Truncated),struct{Stdout string `json:"stdout"`;Stderr string `json:"stderr"`;Error string `json:"error,omitempty"`}{out.Stdout,out.Stderr,out.Error})}()
  if err:=ctx.Err();err!=nil{return nil,Output{},err}
  select{case slots<-struct{}{}:defer func(){<-slots}();default:return &mcp.CallToolResult{},Output{ExitCode:-1,Error:"shell concurrency limit reached"},nil}
  return shellToolFromConfig(ctx,configPath,in)
 })}
 registerNativeTools(server,c,configPath,slots,calls)
 return server
}
func main(){
 if len(os.Args)==2 && (os.Args[1]=="--help"||os.Args[1]=="-h"){fmt.Println("machine: authenticated MCP shell server\nConfiguration: $HOME/.config/machine.yaml");return}
 home,e:=os.UserHomeDir();if e!=nil{log.Fatal(e)}
 configPath:=filepath.Join(home,".config","machine.yaml")
 c,e:=loadConfigWithWarnings(configPath,true);if e!=nil{log.Fatal(e)}
 if e=c.Tunnel.validate();e!=nil{log.Fatal(e)}
 server:=newMachineServer(c,configPath)
 handler:=mcp.NewStreamableHTTPHandler(func(*http.Request)*mcp.Server{return server},&mcp.StreamableHTTPOptions{Stateless:true})
 mux:=http.NewServeMux();mux.Handle("/mcp",protectWithAuth(c.AuthRequired,c.APIKey,handler))
 srv:=&http.Server{Addr:c.Listen,Handler:mux,ReadHeaderTimeout:5*time.Second,ReadTimeout:15*time.Second,IdleTimeout:60*time.Second,MaxHeaderBytes:16384}
 if !c.AuthRequired{log.Print("WARNING: authentication disabled; local clients have shell access")}
 if e=serveMachine(srv,server,c.Tunnel,c.Logging);e!=nil{log.Fatal(e)}
}

func serveMachine(srv *http.Server,server *mcp.Server,tunnel TunnelConfig,logging ...LoggingConfig)error{
 listener,err:=net.Listen("tcp",srv.Addr)
 if err!=nil{return err}
 defer listener.Close()
 ctx,stop:=signal.NotifyContext(context.Background(),os.Interrupt,syscall.SIGTERM);defer stop()
 srv.BaseContext=func(net.Listener)context.Context{return ctx}
 httpDone:=make(chan error,1)
 go func(){httpDone<-srv.Serve(listener)}()
 var tunnelDone chan error
 if tunnel.Enabled{
  tunnelDone=make(chan error,1)
  go func(){tunnelDone<-runTunnel(ctx,server,tunnel,logging...)}()
 }
 log.Printf("machine listening on http://%s/mcp",listener.Addr())
 var failure error
 select{
 case <-ctx.Done():
 case err:=<-httpDone:
  httpDone=nil
  if !errors.Is(err,http.ErrServerClosed){failure=err}
 case err:=<-tunnelDone:
  tunnelDone=nil
  if ctx.Err()==nil{if err==nil{err=errors.New("tunnel supervisor exited unexpectedly")};failure=err}
 }
 stop()
 shutdown,cancel:=context.WithTimeout(context.Background(),8*time.Second);defer cancel()
 if err:=srv.Shutdown(shutdown);err!=nil{_=srv.Close();if failure==nil{failure=err}}
 if tunnelDone!=nil{
  select{case <-tunnelDone:case <-shutdown.Done():if failure==nil{failure=shutdown.Err()}}
 }
 return failure
}
