package main

import (
 "context"
 "errors"
 "log"
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

func runTunnel(ctx context.Context,server *mcp.Server,c TunnelConfig,logging ...LoggingConfig)error{
 cfg,err:=c.sdkConfig();if err!=nil{return err}
 lc:=defaultLogging();if len(logging)>0{lc=logging[0]};cfg.LogLevel,_=logLevel(lc.TunnelLevel)
 st,ct:=mcp.NewInMemoryTransports()
 serverCtx,cancel:=context.WithCancel(ctx);defer cancel()
 done:=make(chan error,1)
 go func(){done<-server.Run(serverCtx,st)}()
 client,err:=tunnelclient.New(cfg,ct);if err!=nil{return err}
 if err=client.Start(ctx);err!=nil{return err}
 defer func(){
  stopCtx,stop:=context.WithTimeout(context.Background(),5*time.Second);defer stop()
  _=client.Stop(stopCtx)
 }()
 readyCtx,stopReady:=context.WithTimeout(ctx,2*time.Minute)
 err=client.WaitUntilReady(readyCtx);stopReady()
 if err!=nil{return err}
 log.Print("OpenAI Secure MCP Tunnel ready")
 select{
 case <-ctx.Done():return nil
 case <-client.Done():return errors.New("tunnel runtime stopped")
 case <-done:return errors.New("in-memory MCP server stopped")
 }
}
