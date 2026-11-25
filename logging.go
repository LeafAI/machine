package main

import (
 "encoding/json"
 "fmt"
 "io"
 "log"
 "log/slog"
 "os"
 "strings"
 "strconv"
 "unicode/utf8"
 "sync/atomic"
 "time"
)

type LoggingConfig struct {
 Level string `yaml:"level"`
 TunnelLevel string `yaml:"tunnel_level"`
 LogArguments bool `yaml:"log_arguments"`
 MaxArgumentBytes int `yaml:"max_argument_bytes"`
 LogOutput bool `yaml:"log_output"`
 MaxOutputBytes int `yaml:"max_output_bytes"`
}
func defaultLogging() LoggingConfig{return LoggingConfig{Level:"info",TunnelLevel:"warn",MaxArgumentBytes:512,MaxOutputBytes:1024}}
func logLevel(s string)(slog.Level,error){
 switch s{case "debug":return slog.LevelDebug,nil;case "info":return slog.LevelInfo,nil;case "warn":return slog.LevelWarn,nil;case "error":return slog.LevelError,nil;default:return 0,fmt.Errorf("invalid logging level")}
}
func(c LoggingConfig) validate()error{
 if _,e:=logLevel(c.Level);e!=nil{return e};if _,e:=logLevel(c.TunnelLevel);e!=nil{return e}
 if c.MaxArgumentBytes<64||c.MaxArgumentBytes>8192{return fmt.Errorf("invalid logging.max_argument_bytes")}
 if c.MaxOutputBytes<64||c.MaxOutputBytes>8192{return fmt.Errorf("invalid logging.max_output_bytes")}
 return nil
}
var callSequence atomic.Uint64
type callLogger struct{cfg LoggingConfig; logger *log.Logger}
func newCallLogger(c LoggingConfig,w io.Writer)*callLogger{return &callLogger{c,log.New(w,"",0)}}
func(l *callLogger) line(level slog.Level,format string,args ...any){
 threshold,_:=logLevel(l.cfg.Level);if level<threshold{return}
 letter:="I";switch level{case slog.LevelDebug:letter="D";case slog.LevelWarn:letter="W";case slog.LevelError:letter="E"}
 l.logger.Printf("%s %s %s",time.Now().Format("06-01 15:04:05"),letter,fmt.Sprintf(format,args...))
}
// Only display known fields; arbitrary nested values and file bodies are never logged.
var argumentFields=map[string][]string{
 "shell":{"command","workdir","timeout_seconds","max_output_bytes","output_mode"},
 "file_read":{"path","start_line","end_line"},
 "file_write":{"path","overwrite","create_parents","expected_sha256"},
 "file_edit":{"path","start_line","end_line","replace_all","expected_sha256"},
 "file_info":{"paths","hash"},
 "file_search":{"path","pattern","context_lines","max_matches"},
 "file_grep":{"paths","pattern","include","context_lines","max_matches","respect_gitignore"},
 "file_glob":{"path","pattern","max_results","respect_gitignore"},
 "directory_list":{"path","depth","include_hidden","max_entries"},
 "batch":{"parallel"},
}
var argumentAliases=map[string]string{"command":"cmd","workdir":"cwd","timeout_seconds":"timeout","max_output_bytes":"limit","output_mode":"mode","max_matches":"max","max_results":"max","max_entries":"max","context_lines":"context","respect_gitignore":"gitignore","include_hidden":"hidden","create_parents":"parents","expected_sha256":"sha256"}
func shortValue(v any)string{
 switch x:=v.(type){
 case string:return strconv.Quote(x)
 case bool:return strconv.FormatBool(x)
 case float64:return strconv.FormatFloat(x,'f',-1,64)
 case []any:
  parts:=make([]string,0,min(len(x),4))
  for _,v:=range x[:min(len(x),4)]{if s,ok:=v.(string);ok{parts=append(parts,strconv.Quote(s))}}
  if len(x)>4{parts=append(parts,fmt.Sprintf("+%d more",len(x)-4))}
  return "["+strings.Join(parts,",")+"]"
 default:return ""
 }
}
func(l *callLogger) arguments(name string,in any)string{
 if !l.cfg.LogArguments{return ""}
 b,e:=json.Marshal(in);if e!=nil{return " args=[unavailable]"}
 var obj map[string]any;if e=json.Unmarshal(b,&obj);e!=nil{return " args=[unavailable]"}
 var parts []string
 for _,key:=range argumentFields[name]{
  v,ok:=obj[key];if !ok||v==nil{continue}
  if key=="expected_sha256"{if s,ok:=v.(string);ok&&len(s)>12{v=s[:12]+"..."}}
  if key=="start_line"{if end,ok:=obj["end_line"];ok{parts=append(parts,"lines="+shortValue(v)+"-"+shortValue(end));continue}}
  if key=="end_line"{if _,ok:=obj["start_line"];ok{continue}}
  label:=argumentAliases[key];if label==""{label=key}
  if val:=shortValue(v);val!=""{parts=append(parts,label+"="+val)}
 }
 if name=="file_write"{if v,ok:=obj["content"].(string);ok{parts=append(parts,fmt.Sprintf("content_bytes=%d",len(v)))}}
 if name=="file_edit"{for _,key:=range []string{"old_string","new_string","content"}{if v,ok:=obj[key].(string);ok{parts=append(parts,fmt.Sprintf("%s_bytes=%d",key,len(v)))}}}
 if name=="batch"{if ops,ok:=obj["operations"].([]any);ok{parts=append([]string{fmt.Sprintf("ops=%d",len(ops))},parts...)}}
 summary:=strings.Join(parts," ")
 if summary==""{return ""}
 if len(summary)>l.cfg.MaxArgumentBytes{
  const suffix=" [truncated]"
  n:=l.cfg.MaxArgumentBytes-len(suffix)
  for n>0&&!utf8.RuneStart(summary[n]){n--}
  summary=summary[:n]+suffix
 }
 return " "+summary
}
// Output previews are opt-in. Quote untrusted text so each record remains one line.
func(l *callLogger) output(v any)string{
 if !l.cfg.LogOutput{return ""}
 b,e:=json.Marshal(v);if e!=nil{return " output=[unavailable]"}
 if len(b)<=l.cfg.MaxOutputBytes{return " output="+string(b)}
 const suffix=" [truncated]"
 // Bound the escaped preview itself, not just the source bytes.
 n:=min(len(b),l.cfg.MaxOutputBytes-len(suffix)-2)
 for n>0{
  for n>0&&!utf8.RuneStart(b[n]){n--}
  q:=strconv.Quote(string(b[:n]))
  if len(q)+len(suffix)<=l.cfg.MaxOutputBytes{return " output="+q+suffix}
  n--
 }
 return " output=[truncated]"
}
func(l *callLogger) begin(name string,in any)func(bool,string,...any){
 id:=callSequence.Add(1);start:=time.Now()
 l.line(slog.LevelInfo,"%04d %s >%s",id,safeToolName(name),l.arguments(name,in))
 return func(ok bool,details string,output ...any){
  level:=slog.LevelInfo;status:="ok";if !ok{level=slog.LevelWarn;status="error"}
  if len(output)>0{details+=l.output(output[0])}
  l.line(level,"%04d %s < %s %s%s",id,safeToolName(name),status,time.Since(start).Round(time.Millisecond),details)
 }
}
func safeToolName(name string)string{if knownTools[name]{return name};return strconv.Quote(name)}
func newServerCallLogger(c Config)*callLogger{
 cfg:=c.Logging;if cfg.Level==""{cfg=defaultLogging()}
 return newCallLogger(cfg,os.Stderr)
}
