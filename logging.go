package main

import (
 "encoding/json"
 "fmt"
 "io"
 "log"
 "log/slog"
 "os"
 "strings"
 "sync/atomic"
 "time"
)

type LoggingConfig struct {
 Level string `yaml:"level"`
 TunnelLevel string `yaml:"tunnel_level"`
 LogArguments bool `yaml:"log_arguments"`
 MaxArgumentBytes int `yaml:"max_argument_bytes"`
}
func defaultLogging() LoggingConfig{return LoggingConfig{Level:"info",TunnelLevel:"warn",MaxArgumentBytes:512}}
func logLevel(s string)(slog.Level,error){
 switch s{case "debug":return slog.LevelDebug,nil;case "info":return slog.LevelInfo,nil;case "warn":return slog.LevelWarn,nil;case "error":return slog.LevelError,nil;default:return 0,fmt.Errorf("invalid logging level")}
}
func(c LoggingConfig) validate()error{
 if _,e:=logLevel(c.Level);e!=nil{return e};if _,e:=logLevel(c.TunnelLevel);e!=nil{return e}
 if c.MaxArgumentBytes<64||c.MaxArgumentBytes>8192{return fmt.Errorf("invalid logging.max_argument_bytes")}
 return nil
}
var callSequence atomic.Uint64
type callLogger struct{cfg LoggingConfig; logger *log.Logger}
func newCallLogger(c LoggingConfig,w io.Writer)*callLogger{return &callLogger{c,log.New(w,"",0)}}
func(l *callLogger) line(level slog.Level,format string,args ...any){
 threshold,_:=logLevel(l.cfg.Level);if level<threshold{return}
 l.logger.Printf("%s %-5s %s",time.Now().Format("2006-01-02 15:04:05.000"),level.String(),fmt.Sprintf(format,args...))
}
func(l *callLogger) arguments(in any)string{
 if !l.cfg.LogArguments{return ""}
 b,e:=json.Marshal(in);if e!=nil{return " args=[unavailable]"}
 var obj map[string]any;if e=json.Unmarshal(b,&obj);e!=nil{return " args=[unavailable]"}
 // Never log file bodies or edit text, even when argument logging is enabled.
 redactArguments(obj)
 b,e=json.Marshal(obj);if e!=nil{return " args=[unavailable]"}
 if len(b)>l.cfg.MaxArgumentBytes{return fmt.Sprintf(" args=%q [truncated]",string(b[:l.cfg.MaxArgumentBytes]))}
 return " args="+string(b)
}
func redactArguments(v any){
 switch x:=v.(type){
 case map[string]any:
  for k,val:=range x{
   name:=strings.ToLower(k)
   switch name{case "content","old_string","new_string","api_key","token","password","authorization","secret","env","environment":x[k]="[redacted]";default:redactArguments(val)}
  }
 case []any:for _,v:=range x{redactArguments(v)}
 }
}
func(l *callLogger) begin(name string,in any)func(bool,string){
 id:=callSequence.Add(1);start:=time.Now()
 l.line(slog.LevelInfo,"call=%d tool=%q start%s",id,name,l.arguments(in))
 return func(ok bool,details string){
  level:=slog.LevelInfo;status:="ok";if !ok{level=slog.LevelWarn;status="error"}
  l.line(level,"call=%d tool=%q status=%s elapsed=%s%s",id,name,status,time.Since(start).Round(time.Millisecond),details)
 }
}
func newServerCallLogger(c Config)*callLogger{
 cfg:=c.Logging;if cfg.Level==""{cfg=defaultLogging()}
 return newCallLogger(cfg,os.Stderr)
}
