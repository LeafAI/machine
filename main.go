package main

import (
 "bytes"
 "context"
 "crypto/sha256"
 "crypto/subtle"
 "errors"
 "fmt"
 "log"
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
 Shell string `yaml:"shell"`
 AdditionalPaths []string `yaml:"additional_paths"`
 TimeoutSeconds int `yaml:"timeout_seconds"`
 MaxOutputBytes int `yaml:"max_output_bytes"`
 MaxConcurrent int `yaml:"max_concurrent"`
}

func loadConfig(path string) (Config,error) {
 c:=Config{Listen:"127.0.0.1:6666",Shell:"/bin/sh",TimeoutSeconds:300,MaxOutputBytes:1048576,MaxConcurrent:4}
 f,e:=os.Open(path); if e!=nil{return c,e}; defer f.Close()
 st,e:=f.Stat(); if e!=nil{return c,e}
 if !st.Mode().IsRegular() || st.Mode().Perm()&0077!=0{return c,errors.New("configuration must be a regular private file (chmod 600)")}
 d:=yaml.NewDecoder(f);d.KnownFields(true)
 if e=d.Decode(&c);e!=nil{return c,e}
 if len(c.APIKey)<32 || strings.ContainsAny(c.APIKey," \t\r\n"){return c,errors.New("api_key must contain at least 32 non-whitespace characters")}
 host,_,e:=net.SplitHostPort(c.Listen);if e!=nil{return c,e}
 ip:=net.ParseIP(host);if ip==nil || !ip.IsLoopback(){return c,errors.New("listen must be a loopback IP; use a secure tunnel")}
 if !filepath.IsAbs(c.Shell)||c.TimeoutSeconds<1||c.TimeoutSeconds>86400||c.MaxOutputBytes<1||c.MaxOutputBytes>16777216||c.MaxConcurrent<1||c.MaxConcurrent>128{return c,errors.New("invalid shell or resource limits")}
 for i,p:=range c.AdditionalPaths {
  p=os.ExpandEnv(p)
  if strings.HasPrefix(p,"~/"){h,e:=os.UserHomeDir();if e!=nil{return c,e};p=filepath.Join(h,p[2:])}
  if !filepath.IsAbs(p)||strings.ContainsRune(p,os.PathListSeparator){return c,errors.New("additional_paths must be absolute directories")}
  c.AdditionalPaths[i]=p
 }
 return c,nil
}

type Input struct {
 Command string `json:"command" jsonschema:"Shell command to execute"`
 Workdir string `json:"workdir,omitempty" jsonschema:"Optional absolute working directory"`
}
type Output struct {
 Stdout string `json:"stdout"`
 Stderr string `json:"stderr"`
 ExitCode int `json:"exit_code"`
 TimedOut bool `json:"timed_out"`
 Truncated bool `json:"truncated"`
 Error string `json:"error,omitempty"`
}
type capped struct{b bytes.Buffer;limit int;truncated bool}
func(w *capped)Write(p []byte)(int,error){
 n:=len(p);remaining:=w.limit-w.b.Len()
 if n>remaining{w.truncated=true;p=p[:remaining]}
 _,_=w.b.Write(p);return n,nil
}
func run(ctx context.Context,c Config,in Input)Output{
 out:=Output{ExitCode:-1}
 if strings.TrimSpace(in.Command)==""{out.Error="command is empty";return out}
 if in.Workdir!=""&&!filepath.IsAbs(in.Workdir){out.Error="workdir must be absolute";return out}
 ctx,cancel:=context.WithTimeout(ctx,time.Duration(c.TimeoutSeconds)*time.Second);defer cancel()
 cmd:=exec.CommandContext(ctx,c.Shell,"-c",in.Command);cmd.Dir=in.Workdir
 paths:=append([]string{},c.AdditionalPaths...)
 if p:=os.Getenv("PATH");p!=""{paths=append(paths,p)}
 cmd.Env=append(os.Environ(),"PATH="+strings.Join(paths,string(os.PathListSeparator)))
 cmd.SysProcAttr=&syscall.SysProcAttr{Setpgid:true}
 cmd.Cancel=func()error{e:=syscall.Kill(-cmd.Process.Pid,syscall.SIGKILL);if errors.Is(e,syscall.ESRCH){return os.ErrProcessDone};return e}
 cmd.WaitDelay=2*time.Second
 stdout,stderr:=&capped{limit:c.MaxOutputBytes},&capped{limit:c.MaxOutputBytes}
 cmd.Stdout=stdout;cmd.Stderr=stderr
 e:=cmd.Run()
 out.Stdout=stdout.b.String();out.Stderr=stderr.b.String();out.Truncated=stdout.truncated||stderr.truncated
 out.TimedOut=errors.Is(ctx.Err(),context.DeadlineExceeded)
 if cmd.ProcessState!=nil{out.ExitCode=cmd.ProcessState.ExitCode()}
 if e!=nil{out.Error=e.Error()};if ctx.Err()!=nil{out.Error=ctx.Err().Error()}
 return out
}
func protect(key string,next http.Handler)http.Handler{
 expected:=sha256.Sum256([]byte("Bearer "+key))
 return http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  if len(r.Header.Values("Origin"))!=0{http.Error(w,"origin forbidden",http.StatusForbidden);return}
  actual:=sha256.Sum256([]byte(r.Header.Get("Authorization")))
  if len(r.Header.Values("Authorization"))!=1||subtle.ConstantTimeCompare(expected[:],actual[:])!=1{
   w.Header().Set("WWW-Authenticate",`Bearer realm="machine"`);http.Error(w,"unauthorized",http.StatusUnauthorized);return
  }
  r.Body=http.MaxBytesReader(w,r.Body,1<<20);w.Header().Set("Cache-Control","no-store");next.ServeHTTP(w,r)
 })
}
func main(){
 if len(os.Args)==2 && (os.Args[1]=="--help"||os.Args[1]=="-h"){fmt.Println("machine: authenticated MCP shell server\nConfiguration: $HOME/.config/machine.yaml");return}
 home,e:=os.UserHomeDir();if e!=nil{log.Fatal(e)}
 c,e:=loadConfig(filepath.Join(home,".config","machine.yaml"));if e!=nil{log.Fatal(e)}
 server:=mcp.NewServer(&mcp.Implementation{Name:"machine",Version:"0.1.0"},nil)
 slots:=make(chan struct{},c.MaxConcurrent)
 mcp.AddTool(server,&mcp.Tool{Name:"shell",Description:"Execute an arbitrary non-interactive shell command with the server user's permissions. May change files and system state."},func(ctx context.Context,_ *mcp.CallToolRequest,in Input)(*mcp.CallToolResult,Output,error){
  select{case slots<-struct{}{}:defer func(){<-slots}();case <-ctx.Done():return nil,Output{},ctx.Err();default:return nil,Output{},fmt.Errorf("shell concurrency limit reached")}
  out:=run(ctx,c,in);return &mcp.CallToolResult{IsError:out.ExitCode!=0||out.Error!=""},out,nil
 })
 handler:=mcp.NewStreamableHTTPHandler(func(*http.Request)*mcp.Server{return server},&mcp.StreamableHTTPOptions{Stateless:true})
 mux:=http.NewServeMux();mux.Handle("/mcp",protect(c.APIKey,handler))
 srv:=&http.Server{Addr:c.Listen,Handler:mux,ReadHeaderTimeout:5*time.Second,ReadTimeout:15*time.Second,IdleTimeout:60*time.Second,MaxHeaderBytes:16384}
 ctx,stop:=signal.NotifyContext(context.Background(),os.Interrupt,syscall.SIGTERM);defer stop()
 srv.BaseContext=func(net.Listener)context.Context{return ctx}
 go func(){<-ctx.Done();shutdown,cancel:=context.WithTimeout(context.Background(),5*time.Second);defer cancel();_=srv.Shutdown(shutdown)}()
 log.Printf("machine listening on http://%s/mcp",c.Listen)
 if e=srv.ListenAndServe();e!=nil&&!errors.Is(e,http.ErrServerClosed){log.Fatal(e)}
}
