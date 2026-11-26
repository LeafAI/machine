package main

import (
 "context"
 "encoding/binary"
 "errors"
 "io"
 "net"
 "net/http"
 "net/url"
 "os"
 "strconv"
 "strings"
 "time"

 "golang.org/x/net/http/httpproxy"
)

type ProxyConfig struct {
 Enabled bool `yaml:"enabled"`
 URL string `yaml:"url"`
 NoProxy []string `yaml:"no_proxy"`
 Tunnel bool `yaml:"tunnel"`
 Shell bool `yaml:"shell"`
}
func defaultProxy() ProxyConfig{return ProxyConfig{Tunnel:true,Shell:true,NoProxy:[]string{"localhost","127.0.0.1","::1"}}}
func(p *ProxyConfig) validate()error{
 if !p.Enabled{return nil}
 var missing bool
 p.URL=os.Expand(p.URL,func(k string)string{v,ok:=os.LookupEnv(k);if !ok{missing=true};return v})
 if missing{return errors.New("proxy.url references missing environment variable")}
 u,e:=url.Parse(p.URL)
 if e!=nil||u.Hostname()==""||u.Port()==""||u.Opaque!=""||u.Path!=""||u.RawQuery!=""||u.Fragment!=""{return errors.New("proxy.url must specify scheme, host and port only")}
 port,e:=strconv.Atoi(u.Port());if e!=nil||port<1||port>65535{return errors.New("invalid proxy port")}
 switch u.Scheme{case "http","https","socks5","socks5h","socks4","socks4a":default:return errors.New("unsupported proxy scheme")}
 if u.Scheme=="socks4"||u.Scheme=="socks4a"{
  if u.User!=nil{if _,ok:=u.User.Password();ok{return errors.New("SOCKS4 supports user ID, not password")};if strings.ContainsRune(u.User.Username(),0)||len(u.User.Username())>255{return errors.New("invalid SOCKS4 user ID")}}
 }
 for _,v:=range p.NoProxy{if strings.ContainsAny(v,"\r\n\x00,"){return errors.New("invalid proxy.no_proxy entry")}}
 return nil
}
func(p ProxyConfig) bypass()string{return strings.Join(append([]string{"localhost","127.0.0.0/8","::1"},p.NoProxy...),",")}
func(p ProxyConfig) shellEnv(env []string)[]string{
 if !p.Enabled||!p.Shell{return env}
 result:=make([]string,0,len(env)+8)
 for _,entry:=range env{k,_,_:=strings.Cut(entry,"=");switch strings.ToLower(k){case "http_proxy","https_proxy","all_proxy","no_proxy":continue};result=append(result,entry)}
 for _,k:=range []string{"HTTP_PROXY","HTTPS_PROXY","ALL_PROXY","http_proxy","https_proxy","all_proxy"}{result=append(result,k+"="+p.URL)}
 return append(result,"NO_PROXY="+p.bypass(),"no_proxy="+p.bypass())
}
// The embedded tunnel SDK clones http.DefaultTransport. Install once, before
// constructing the SDK or starting any goroutines. Do not mutate it on reload.
func(p ProxyConfig) configureTunnel()error{
 if !p.Enabled||!p.Tunnel{return nil}
 base,ok:=http.DefaultTransport.(*http.Transport);if !ok{return errors.New("unsupported default HTTP transport")}
 t:=base.Clone()
 u,e:=url.Parse(p.URL);if e!=nil{return errors.New("invalid proxy URL")}
 route:=(&httpproxy.Config{HTTPProxy:p.URL,HTTPSProxy:p.URL,NoProxy:p.bypass()}).ProxyFunc()
 if u.Scheme=="socks4"||u.Scheme=="socks4a"{
  t.Proxy=nil
  dial:=&net.Dialer{Timeout:30*time.Second,KeepAlive:30*time.Second}
  t.DialContext=func(ctx context.Context,network,addr string)(net.Conn,error){
   target:=&url.URL{Scheme:"https",Host:addr}
   proxy,e:=route(target);if e!=nil{return nil,e}
   if proxy==nil{return dial.DialContext(ctx,network,addr)}
   return dialSOCKS4(ctx,dial,u,addr)
  }
 }else{
  t.Proxy=func(req *http.Request)(*url.URL,error){return route(req.URL)}
 }
 http.DefaultTransport=t
 return nil
}
func dialSOCKS4(ctx context.Context,dial *net.Dialer,proxy *url.URL,addr string)(net.Conn,error){
 host,port,e:=net.SplitHostPort(addr);if e!=nil{return nil,e}
 n,e:=strconv.Atoi(port);if e!=nil||n<1||n>65535{return nil,errors.New("invalid SOCKS4 destination port")}
 ip:=net.ParseIP(host);remoteDNS:=false
 if ip==nil{
  if proxy.Scheme=="socks4a"{remoteDNS=true;ip=net.IPv4(0,0,0,1)}else{
   ips,e:=net.DefaultResolver.LookupIP(ctx,"ip4",host);if e!=nil{return nil,e};if len(ips)==0{return nil,errors.New("SOCKS4 requires IPv4 destination")};ip=ips[0]
  }
 }
 ip=ip.To4();if ip==nil{return nil,errors.New("SOCKS4 requires IPv4 destination")}
 if len(host)>253||strings.ContainsRune(host,0){return nil,errors.New("invalid SOCKS4 destination")}
 user:="";if proxy.User!=nil{user=proxy.User.Username()}
 req:=[]byte{4,1,0,0};binary.BigEndian.PutUint16(req[2:],uint16(n));req=append(req,ip...);req=append(req,user...);req=append(req,0)
 if remoteDNS{req=append(req,host...);req=append(req,0)}
 conn,e:=dial.DialContext(ctx,"tcp",proxy.Host);if e!=nil{return nil,e}
 success:=false;defer func(){if !success{conn.Close()}}()
 deadline:=time.Now().Add(30*time.Second);if d,ok:=ctx.Deadline();ok&&d.Before(deadline){deadline=d}
 if e=conn.SetDeadline(deadline);e!=nil{return nil,e}
 stopped:=make(chan struct{});stop:=context.AfterFunc(ctx,func(){conn.Close();close(stopped)})
 defer func(){if !stop(){<-stopped}}()
 if _,e=io.Copy(conn,strings.NewReader(string(req)));e!=nil{return nil,e}
 var reply [8]byte;if _,e=io.ReadFull(conn,reply[:]);e!=nil{return nil,e}
 if reply[0]!=0||reply[1]!=90{return nil,errors.New("SOCKS4 proxy rejected connection")}
 if e=ctx.Err();e!=nil{return nil,e}
 if e=conn.SetDeadline(time.Time{});e!=nil{return nil,e}
 success=true;return conn,nil
}
