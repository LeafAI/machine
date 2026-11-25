package main

import (
 "context"
 "errors"
 "fmt"
 "log/slog"
 "os"
 "regexp"
 "strings"
 "time"

 "github.com/modelcontextprotocol/go-sdk/mcp"
 tunnelclient "github.com/openai/tunnel-client"
)

type TunnelConfig struct {
 Enabled bool `yaml:"enabled"`
 ID string `yaml:"id"`
 APIKey string `yaml:"api_key"`
 OrganizationID string `yaml:"organization_id"`
}

var tunnelIDPattern=regexp.MustCompile(`^tunnel_([a-z0-9]{4}_)?[a-z0-9]{32}$`)

func (c TunnelConfig) sdkConfig() (tunnelclient.Config,error) {
 id:=strings.TrimSpace(c.ID)
 if id=="" {id=strings.TrimSpace(os.Getenv("CONTROL_PLANE_TUNNEL_ID"))}
 if !tunnelIDPattern.MatchString(id){return tunnelclient.Config{},errors.New("valid tunnel ID required")}
 key:=strings.TrimSpace(os.Getenv("CONTROL_PLANE_API_KEY"))
 if key==""{key=strings.TrimSpace(c.APIKey)}
 if key==""{return tunnelclient.Config{},errors.New("tunnel.api_key or CONTROL_PLANE_API_KEY runtime key required")}
 org:=strings.TrimSpace(c.OrganizationID)
 if org==""{org=strings.TrimSpace(os.Getenv("CONTROL_PLANE_ORGANIZATION_ID"))}
 return tunnelclient.Config{TunnelID:id,APIKey:key,OrganizationID:org},nil
}

func(c TunnelConfig) validate()error{
 if !c.Enabled{return nil}
 _,err:=c.sdkConfig();return err
}

// Do not directly pass tunnel credentials into shell subprocesses.
// This is not a security boundary: commands run with the same OS privileges.
func shellEnvironment(env []string) []string {
 result:=make([]string,0,len(env))
 for _,entry:=range env{
  name,_,_:=strings.Cut(entry,"=")
  if strings.HasPrefix(name,"CONTROL_PLANE_")||name=="OPENAI_ADMIN_KEY"||name=="OPENAI_API_KEY"{continue}
  result=append(result,entry)
 }
 return result
}

// superviseTunnel retries a failed tunnel generation without terminating the
// HTTP listener or replaying tool calls. Each generation owns its transports.
func superviseTunnel(ctx context.Context,attempt func(context.Context)error,report func(error,time.Duration),minDelay,maxDelay time.Duration){
 delay:=minDelay
 for ctx.Err()==nil{
  started:=time.Now()
  err:=attempt(ctx)
  if ctx.Err()!=nil{return}
  if time.Since(started)>=maxDelay{delay=minDelay}
  if err==nil{err=errors.New("tunnel generation ended")}
  report(err,delay)
  timer:=time.NewTimer(delay)
  select{case <-ctx.Done():timer.Stop();return;case <-timer.C:}
  delay=min(delay*2,maxDelay)
 }
}

func runTunnel(ctx context.Context,server *mcp.Server,c TunnelConfig,logging ...LoggingConfig)error{
 cfg,err:=c.sdkConfig();if err!=nil{return err}
 lc:=defaultLogging();if len(logging)>0{lc=logging[0]};cfg.LogLevel,_=logLevel(lc.TunnelLevel)
 logger:=newCallLogger(lc,os.Stderr)
 superviseTunnel(ctx,func(ctx context.Context)error{return runTunnelGeneration(ctx,server,cfg,logger)},func(err error,delay time.Duration){
  // SDK errors may include identifiers or credentials. Redact the runtime key.
  reason:=strings.ReplaceAll(err.Error(),cfg.APIKey,"[redacted]")
  if len(reason)>512{reason=reason[:512]}
  logger.line(slog.LevelWarn,"tunnel disconnected reason=%q retry_in=%s",reason,delay)
 },time.Second,30*time.Second)
 return nil
}

func runTunnelGeneration(ctx context.Context,server *mcp.Server,cfg tunnelclient.Config,logger *callLogger)(failure error){
 st,ct:=mcp.NewInMemoryTransports()
 generation,cancel:=context.WithCancel(ctx)
 defer cancel()
 // Connect before starting the client, and wait for the session to close
 // before creating another generation.
 session,err:=server.Connect(generation,st,nil)
 if err!=nil{return fmt.Errorf("MCP session connect: %w",err)}
 done:=make(chan error,1)
 go func(){done<-session.Wait()}()
 defer func(){
  cancel()
  if err:=session.Close();err!=nil&&ctx.Err()==nil{failure=errors.Join(failure,fmt.Errorf("MCP session close: %w",err))}
  if done!=nil{<-done}
 }()
 client,err:=tunnelclient.New(cfg,ct);if err!=nil{return fmt.Errorf("tunnel construct: %w",err)}
 if err=client.Start(generation);err!=nil{return fmt.Errorf("tunnel start: %w",err)}
 defer func(){
  cancel()
  stopCtx,stop:=context.WithTimeout(context.Background(),5*time.Second);defer stop()
  if err:=client.Stop(stopCtx);err!=nil&&ctx.Err()==nil{failure=errors.Join(failure,fmt.Errorf("tunnel stop: %w",err))}
 }()
 readyCtx,stopReady:=context.WithTimeout(generation,2*time.Minute)
 defer stopReady()
 select{
 case <-ctx.Done():return nil
 case <-readyCtx.Done():return fmt.Errorf("tunnel readiness: %w",readyCtx.Err())
 case sig:=<-client.Done():return fmt.Errorf("tunnel runtime shutdown before ready: %v",sig)
 case err:=<-done:done=nil;return sessionEnded(err)
 case <-client.Ready():logger.line(slog.LevelInfo,"tunnel ready")
 }
 select{
 case <-ctx.Done():return nil
 case sig:=<-client.Done():return fmt.Errorf("tunnel runtime shutdown: %v",sig)
 case err:=<-done:done=nil;return sessionEnded(err)
 }
}
func sessionEnded(err error)error{
 if err==nil{return errors.New("in-memory MCP session closed by peer")}
 return fmt.Errorf("in-memory MCP session ended: %w",err)
}
