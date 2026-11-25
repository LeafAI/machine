package main

import (
 "bytes"
 "strings"
 "testing"
)

func TestCompactCallLogging(t *testing.T){
 var b bytes.Buffer
 c:=defaultLogging();c.LogArguments=true
 l:=newCallLogger(c,&b)
 finish:=l.begin("file_edit",EditInput{Path:"/tmp/test",OldString:"secret old",NewString:"secret new"})
 finish(true,"")
 s:=b.String()
 for _,want:=range []string{"tool=\"file_edit\"","start","/tmp/test","[redacted]","status=ok","elapsed="}{if !strings.Contains(s,want){t.Fatalf("missing %q: %s",want,s)}}
 if strings.Contains(s,"secret old")||strings.Contains(s,"secret new"){t.Fatal("edit content leaked")}
 b.Reset()
 l.begin("shell",Input{Command:"echo first\nsecond"}) (false," exit=1")
 if strings.Count(b.String(),"\n")!=2||!strings.Contains(b.String(),"status=error"){t.Fatalf("unexpected lines: %s",b.String())}
}
func TestLogArgumentOptInAndBounds(t *testing.T){
 c:=defaultLogging();var b bytes.Buffer
 l:=newCallLogger(c,&b)
 if s:=l.arguments(Input{Command:"sensitive"});s!=""{t.Fatal("arguments enabled by default")}
 c.LogArguments=true;c.MaxArgumentBytes=64
 l=newCallLogger(c,&b)
 s:=l.arguments(Input{Command:strings.Repeat("a",10000)})
 if len(s)>160||!strings.Contains(s,"truncated"){t.Fatal("missing argument limit")}
 s=l.arguments(map[string]any{"operations":[]any{map[string]any{"arguments":map[string]any{"content":"hidden-body"}}}})
 if strings.Contains(s,"hidden-body"){t.Fatal("nested content leaked")}
}
