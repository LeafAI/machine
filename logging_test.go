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
 for _,want:=range []string{" I ","file_edit >","/tmp/test","old_string_bytes=10","new_string_bytes=10","file_edit < ok"}{if !strings.Contains(s,want){t.Fatalf("missing %q: %s",want,s)}}
 if strings.Contains(s,"secret old")||strings.Contains(s,"secret new"){t.Fatal("edit content leaked")}
 b.Reset()
 l.begin("shell",Input{Command:"echo first\nsecond"}) (false," exit=1")
 if strings.Count(b.String(),"\n")!=2||!strings.Contains(b.String(),"shell < error"){t.Fatalf("unexpected lines: %s",b.String())}
}
func TestLogArgumentOptInAndBounds(t *testing.T){
 c:=defaultLogging();var b bytes.Buffer
 l:=newCallLogger(c,&b)
 if s:=l.arguments("shell",Input{Command:"sensitive"});s!=""{t.Fatal("arguments enabled by default")}
 c.LogArguments=true;c.MaxArgumentBytes=64
 l=newCallLogger(c,&b)
 s:=l.arguments("shell",Input{Command:strings.Repeat("a",10000)})
 if len(s)>160||!strings.Contains(s,"truncated"){t.Fatal("missing argument limit")}
 s=l.arguments("batch",map[string]any{"operations":[]any{map[string]any{"arguments":map[string]any{"content":"hidden-body"}}}})
 if strings.Contains(s,"hidden-body"){t.Fatal("nested content leaked")}
}

func TestShortArgumentSummary(t *testing.T){
 c:=defaultLogging();c.LogArguments=true;var b bytes.Buffer;l:=newCallLogger(c,&b)
 if got:=l.arguments("file_read",ReadInput{Path:"main.go",StartLine:1,EndLine:80});got!=" path=\"main.go\" lines=1-80"{t.Fatal(got)}
 if got:=l.arguments("shell",Input{Command:"go test ./...",Workdir:"/tmp"});got!=" cmd=\"go test ./...\" cwd=\"/tmp\""{t.Fatal(got)}
}

func TestOutputPreview(t *testing.T){
 c:=defaultLogging();var b bytes.Buffer
 l:=newCallLogger(c,&b)
 if l.output("secret")!=""{t.Fatal("output enabled by default")}
 c.LogOutput=true;c.MaxOutputBytes=64;l=newCallLogger(c,&b)
 l.begin("shell",Input{Command:"printf ok"})(true,"",map[string]string{"stdout":"ok\n"})
 if !strings.Contains(b.String(),`output={"stdout":"ok\n"}`)||strings.Count(b.String(),"\n")!=2{t.Fatal(b.String())}
 s:=l.output(strings.Repeat("日\n",1000))
 if len(s)>64+len(" output=")||!strings.Contains(s,"[truncated]")||strings.Contains(s,"\n"){t.Fatal(s)}
}
