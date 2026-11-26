package main

import (
 "context"
 "io"
 "net"
 "net/http"
 "net/url"
 "strings"
 "testing"
 "time"
)

func TestProxyConfig(t *testing.T){
 for _,raw:=range []string{"http://127.0.0.1:7890","https://user:pass@localhost:443","socks5://localhost:7891","socks5h://localhost:7891","socks4://localhost:1080","socks4a://user@localhost:1080"}{
  p:=defaultProxy();p.Enabled=true;p.URL=raw;if e:=p.validate();e!=nil{t.Fatalf("valid proxy rejected: %v",e)}
 }
 for _,raw:=range []string{"ftp://localhost:123","http://localhost","http://localhost:99999","http://localhost:80/path","socks4://user:pass@localhost:1080","http://:80","http://localhost:80?secret=x"}{
  p:=defaultProxy();p.Enabled=true;p.URL=raw;if e:=p.validate();e==nil{t.Fatal("invalid proxy accepted")}
 }
 t.Setenv("MACHINE_TEST_PROXY","socks5://localhost:1080")
 p:=defaultProxy();p.Enabled=true;p.URL="${MACHINE_TEST_PROXY}"
 if e:=p.validate();e!=nil||p.URL!="socks5://localhost:1080"{t.Fatal("environment expansion failed")}
}
func TestProxyShellEnvironment(t *testing.T){
 p:=defaultProxy();p.Enabled=true;p.URL="socks5://localhost:1080"
 env:=p.shellEnv([]string{"HTTPS_PROXY=old","http_proxy=old","NO_PROXY=old","PATH=/bin"})
 s:=strings.Join(env,"\n")
 if strings.Contains(s,"=old")||!strings.Contains(s,"HTTPS_PROXY="+p.URL)||!strings.Contains(s,"all_proxy="+p.URL)||!strings.Contains(s,"PATH=/bin")||!strings.Contains(s,"127.0.0.0/8"){t.Fatal("unexpected proxy environment")}
 p.Shell=false;if got:=p.shellEnv([]string{"HTTPS_PROXY=old"});len(got)!=1||got[0]!="HTTPS_PROXY=old"{t.Fatal("shell opt-out changed inherited environment")}
}
func TestProxyHTTPRoute(t *testing.T){
 orig:=http.DefaultTransport;defer func(){http.DefaultTransport=orig}()
 p:=defaultProxy();p.Enabled=true;p.URL="http://localhost:7890";p.NoProxy=[]string{"example.org"}
 if e:=p.configureTunnel();e!=nil{t.Fatal(e)}
 tr:=http.DefaultTransport.(*http.Transport)
 for _,v:=range []struct{target string;proxied bool}{{"https://api.openai.com",true},{"http://127.0.0.1:6666",false},{"https://example.org",false}}{
  u,_:=url.Parse(v.target);got,e:=tr.Proxy(&http.Request{URL:u});if e!=nil||(got!=nil)!=v.proxied{t.Fatalf("route %s: %v",v.target,e)}
 }
}
func TestSOCKS4aHandshake(t *testing.T){
 ln,e:=net.Listen("tcp","127.0.0.1:0");if e!=nil{t.Fatal(e)};defer ln.Close()
 want:=append([]byte{4,1,1,187,0,0,0,1},[]byte("user\x00example.org\x00")...)
 seen:=make(chan string,1)
 go func(){c,e:=ln.Accept();if e!=nil{seen<-e.Error();return};defer c.Close();_ = c.SetDeadline(time.Now().Add(time.Second));b:=make([]byte,len(want));if _,e=io.ReadFull(c,b);e!=nil{seen<-e.Error();return};seen<-string(b);_,_=c.Write([]byte{0,90,0,0,0,0,0,0})}()
 u,_:=url.Parse("socks4a://user@"+ln.Addr().String())
 ctx,cancel:=context.WithTimeout(context.Background(),2*time.Second);defer cancel()
 c,e:=dialSOCKS4(ctx,&net.Dialer{},u,"example.org:443");if e!=nil{t.Fatal(e)};defer c.Close()
 if got:=<-seen;got!=string(want){t.Fatal("incorrect SOCKS4a request")}
}
