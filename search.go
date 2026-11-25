package main

import (
 "context"
 "crypto/rand"
 "encoding/hex"
 "errors"
 "io/fs"
 "io"
 "sort"
 "path"
 "path/filepath"
 "regexp"
 "strings"
 "syscall"
)

func nonblockFlag()int{return syscall.O_NONBLOCK}
func randomSuffix()string{var b [16]byte;_,_=rand.Read(b[:]);return hex.EncodeToString(b[:])}
type ListInput struct{Path string `json:"path"`;Depth int `json:"depth,omitempty"`;IncludeHidden bool `json:"include_hidden,omitempty"`;MaxEntries int `json:"max_entries,omitempty"`}
type GlobInput struct{Path string `json:"path"`;Pattern string `json:"pattern"`;MaxResults int `json:"max_results,omitempty"`;RespectGitignore *bool `json:"respect_gitignore,omitempty"`}
type ListEntry struct{Path string `json:"path"`;Type string `json:"type"`;Size int64 `json:"size"`}
type ListOutput struct{Entries []ListEntry `json:"entries"`;Truncated bool `json:"truncated"`}
type GlobOutput struct{Paths []string `json:"paths"`;Truncated bool `json:"truncated"`}
func limitValue(n,max int)int{if n<=0||n>max{return max};return n}
func globMatch(pattern,name string)bool{
 a,b:=strings.Split(pattern,"/"),strings.Split(name,"/")
 var match func(int,int)bool
 memo:=map[[2]int]bool{};seen:=map[[2]int]bool{}
 match=func(i,j int)bool{k:=[2]int{i,j};if seen[k]{return memo[k]};seen[k]=true
  ok:=false
  if i==len(a){ok=j==len(b)}else if a[i]=="**"{ok=match(i+1,j)||(j<len(b)&&match(i,j+1))}else if j<len(b){m,_:=path.Match(a[i],b[j]);ok=m&&match(i+1,j+1)}
  memo[k]=ok;return ok
 };return match(0,0)
}
func validateGlob(p string)error{if p==""||len(p)>1024{return errors.New("invalid glob")};for _,s:=range strings.Split(p,"/"){if _,e:=path.Match(s,"");e!=nil{return e}};return nil}
type ignoreRule struct{base,pattern string;negate,dirOnly,anchored bool}
// A bounded subset of gitignore syntax, supporting nested files, negation,
// directory-only patterns, anchoring and **. Escaped whitespace is not supported.
func loadIgnore(l fileLocation,rel string)[]ignoreRule{
 name:=filepath.Join(l.name,filepath.FromSlash(rel),".gitignore")
 b,_,e:=readText(fileLocation{l.root,name,""},65536);if e!=nil{return nil}
 var rules []ignoreRule
 for _,line:=range strings.Split(string(b),"\n"){line=strings.TrimSpace(line);if line==""||strings.HasPrefix(line,"#"){continue}
  r:=ignoreRule{base:rel,negate:strings.HasPrefix(line,"!"),dirOnly:strings.HasSuffix(line,"/")}
  line=strings.TrimPrefix(line,"!");r.anchored=strings.HasPrefix(line,"/");r.pattern=strings.Trim(line,"/")
  if validateGlob(r.pattern)==nil{rules=append(rules,r)}
 };return rules
}
func ignored(rules []ignoreRule,name string,isDir bool)bool{
 excluded:=false
 for _,r:=range rules{
  rel:=name;if r.base!="."{if !strings.HasPrefix(name,r.base+"/"){continue};rel=strings.TrimPrefix(name,r.base+"/")}
  if r.dirOnly&&!isDir{continue}
  candidate:=rel;if !r.anchored&&!strings.Contains(r.pattern,"/"){candidate=path.Base(rel)}
  if globMatch(r.pattern,candidate){excluded=!r.negate}
 };return excluded
}
var errWalkLimit=errors.New("walk limit reached")
func walkFiles(ctx context.Context,l fileLocation,depth int,hidden,ignore bool,visit func(string,fs.FileInfo)error)error{
 var walk func(string,int,[]ignoreRule)error
 count:=0
 walk=func(rel string,d int,rules []ignoreRule)error{
  if e:=ctx.Err();e!=nil{return e}
  if ignore{rules=append(append([]ignoreRule{},rules...),loadIgnore(l,rel)...)}
  // Read incrementally rather than loading arbitrarily large directories.
  dir,e:=l.root.Open(filepath.Join(l.name,filepath.FromSlash(rel)));if e!=nil{return e};defer dir.Close()
  for {
   entries,e:=dir.ReadDir(128)
   for _,entry:=range entries{
    if x:=ctx.Err();x!=nil{return x};count++;if count>100000{return errWalkLimit}
    name:=entry.Name();if name==".git"||(!hidden&&strings.HasPrefix(name,".")){continue}
    p:=path.Join(rel,name);if entry.Type()&fs.ModeSymlink!=0{continue}
    st,x:=l.root.Lstat(filepath.Join(l.name,filepath.FromSlash(p)));if x!=nil{return x}
    if st.Mode()&fs.ModeSymlink!=0||(!st.IsDir()&&!st.Mode().IsRegular()){continue}
    if ignore&&ignored(rules,p,st.IsDir()){continue}
    if x=visit(p,st);x!=nil{return x}
    if st.IsDir()&&d<depth{if x=walk(p,d+1,rules);x!=nil{return x}}
   }
   if e!=nil{if errors.Is(e,io.EOF){return nil};return e}
  }
 }
 return walk(".",1,nil)
}
func directoryList(ctx context.Context,c Config,in ListInput)(ListOutput,error){
 out:=ListOutput{Entries:[]ListEntry{}}
 l,e:=c.Filesystem.locate(in.Path);if e!=nil{return out,e};defer l.close()
 depth:=in.Depth;if depth<=0{depth=1};if depth>64{return out,errors.New("depth exceeds 64")}
 max:=limitValue(in.MaxEntries,c.Filesystem.MaxSearchResults)
 e=walkFiles(ctx,l,depth,in.IncludeHidden,false,func(p string,st fs.FileInfo)error{if len(out.Entries)>=max{return errWalkLimit};out.Entries=append(out.Entries,ListEntry{p,kind(st),st.Size()});return nil})
 if errors.Is(e,errWalkLimit){out.Truncated=true;e=nil};sort.Slice(out.Entries,func(i,j int)bool{return out.Entries[i].Path<out.Entries[j].Path});return out,e
}
func fileGlob(ctx context.Context,c Config,in GlobInput)(GlobOutput,error){
 out:=GlobOutput{Paths:[]string{}}
 if e:=validateGlob(in.Pattern);e!=nil{return out,e}
 l,e:=c.Filesystem.locate(in.Path);if e!=nil{return out,e};defer l.close()
 ignore:=c.Filesystem.RespectGitignore;if in.RespectGitignore!=nil{ignore=*in.RespectGitignore}
 max:=limitValue(in.MaxResults,c.Filesystem.MaxSearchResults)
 e=walkFiles(ctx,l,64,true,ignore,func(p string,st fs.FileInfo)error{if st.Mode().IsRegular()&&globMatch(in.Pattern,p){if len(out.Paths)>=max{return errWalkLimit};out.Paths=append(out.Paths,p)};return nil})
 if errors.Is(e,errWalkLimit){out.Truncated=true;e=nil};sort.Strings(out.Paths);return out,e
}
type SearchInput struct{Path string `json:"path"`;Pattern string `json:"pattern"`;ContextLines int `json:"context_lines,omitempty"`;MaxMatches int `json:"max_matches,omitempty"`}
type GrepInput struct{Paths []string `json:"paths"`;Pattern string `json:"pattern"`;Include []string `json:"include,omitempty"`;ContextLines int `json:"context_lines,omitempty"`;MaxMatches int `json:"max_matches,omitempty"`;RespectGitignore *bool `json:"respect_gitignore,omitempty"`}
type Match struct{Path string `json:"path"`;Line int `json:"line"`;Content string `json:"content"`;Before []string `json:"before,omitempty"`;After []string `json:"after,omitempty"`}
type SearchOutput struct{Matches []Match `json:"matches"`;Truncated bool `json:"truncated"`;Skipped int `json:"skipped_files"`}
func fileSearch(ctx context.Context,c Config,in SearchInput)(SearchOutput,error){return fileGrep(ctx,c,GrepInput{Paths:[]string{in.Path},Pattern:in.Pattern,ContextLines:in.ContextLines,MaxMatches:in.MaxMatches})}
func fileGrep(ctx context.Context,c Config,in GrepInput)(SearchOutput,error){
 out:=SearchOutput{Matches:[]Match{}}
 if len(in.Pattern)>4096||len(in.Paths)==0||len(in.Paths)>64||in.ContextLines<0||in.ContextLines>10{return out,errors.New("invalid search parameters")}
 re,e:=regexp.Compile(in.Pattern);if e!=nil{return out,e}
 for _,p:=range in.Include{if e=validateGlob(p);e!=nil{return out,e}}
 max:=limitValue(in.MaxMatches,c.Filesystem.MaxSearchResults);budget:=c.Filesystem.MaxReadBytes;scanned:=0
 scan:=func(l fileLocation)error{
  if e:=ctx.Err();e!=nil{return e}
  b,_,e:=readText(l,c.Filesystem.MaxReadBytes);if e!=nil{out.Skipped++;return nil}
  scanned+=len(b);if scanned>64<<20{return errWalkLimit}
  lines:=linesOf(string(b))
  for i,line:=range lines{
   if e:=ctx.Err();e!=nil{return e};if !re.MatchString(strings.TrimRight(line,"\r\n")){continue}
   before:=lines[maxInt(0,i-in.ContextLines):i];after:=lines[i+1:min(i+1+in.ContextLines,len(lines))]
   cost:=len(line)+len(l.absolute)+128;for _,s:=range before{cost+=len(s)};for _,s:=range after{cost+=len(s)}
   if len(out.Matches)>=max||cost>budget{return errWalkLimit};budget-=cost
   out.Matches=append(out.Matches,Match{l.absolute,i+1,strings.TrimRight(line,"\r\n"),before,after})
  };return nil
 }
 ignore:=c.Filesystem.RespectGitignore;if in.RespectGitignore!=nil{ignore=*in.RespectGitignore}
 for _,p:=range in.Paths{
  l,e:=c.Filesystem.locate(p);if e!=nil{return out,e}
  st,e:=l.root.Lstat(l.name);if e==nil&&st.IsDir(){e=walkFiles(ctx,l,64,true,ignore,func(rel string,st fs.FileInfo)error{
   if !st.Mode().IsRegular(){return nil}
   if len(in.Include)>0{ok:=false;for _,g:=range in.Include{if globMatch(g,rel){ok=true;break}};if !ok{return nil}}
   return scan(fileLocation{l.root,filepath.Join(l.name,filepath.FromSlash(rel)),filepath.Join(l.absolute,filepath.FromSlash(rel))})
  })}else if e==nil{e=scan(l)};l.close()
  if errors.Is(e,errWalkLimit){out.Truncated=true;return out,nil};if e!=nil{return out,e}
 };return out,nil
}
func maxInt(a,b int)int{if a>b{return a};return b}
