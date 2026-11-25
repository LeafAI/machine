package main

import (
 "context"
 "bytes"
 "crypto/sha256"
 "encoding/hex"
 "errors"
 "fmt"
 "io"
 "io/fs"
 "os"
 "path/filepath"
 "strings"
 "sync"
 "unicode/utf8"
)

type ToolConfig struct { Enabled []string `yaml:"enabled"` }
type FilesystemConfig struct {
 DefaultWorkdir string `yaml:"default_workdir"`
 AllowedRoots []string `yaml:"allowed_roots"`
 MaxReadBytes int `yaml:"max_read_bytes"`
 MaxWriteBytes int `yaml:"max_write_bytes"`
 MaxSearchResults int `yaml:"max_search_results"`
 RespectGitignore bool `yaml:"respect_gitignore"`
 FollowSymlinks bool `yaml:"follow_symlinks"`
}
type BatchConfig struct {
 MaxOperations int `yaml:"max_operations"`
 MaxParallel int `yaml:"max_parallel"`
 MaxOutputBytes int `yaml:"max_output_bytes"`
}
func defaultFilesystem() FilesystemConfig {return FilesystemConfig{MaxReadBytes:1048576,MaxWriteBytes:4194304,MaxSearchResults:200,RespectGitignore:true}}
func defaultBatch() BatchConfig{return BatchConfig{MaxOperations:16,MaxParallel:4,MaxOutputBytes:262144}}
var knownTools=map[string]bool{"shell":true,"file_read":true,"file_write":true,"file_edit":true,"file_info":true,"file_search":true,"file_grep":true,"file_glob":true,"directory_list":true,"batch":true}
func (c Config) enabled(name string)bool{for _,n:=range c.Tools.Enabled{if n==name{return true}};return false}
func expandPath(p string)(string,error){
 p=os.ExpandEnv(p)
 if p=="~"||strings.HasPrefix(p,"~/"){h,e:=os.UserHomeDir();if e!=nil{return "",e};p=filepath.Join(h,strings.TrimPrefix(strings.TrimPrefix(p,"~"),"/"))}
 if !filepath.IsAbs(p){return "",errors.New("filesystem configuration paths must be absolute")}
 return filepath.Clean(p),nil
}
func(c *Config) validateTools()error{
 seen:=map[string]bool{}
 for _,n:=range c.Tools.Enabled{if !knownTools[n]||seen[n]{return errors.New("unknown or duplicate enabled tool")};seen[n]=true}
 f:=&c.Filesystem
 if f.MaxReadBytes<1||f.MaxReadBytes>16777216||f.MaxWriteBytes<1||f.MaxWriteBytes>16777216||f.MaxSearchResults<1||f.MaxSearchResults>10000{return errors.New("invalid filesystem limits")}
 if f.FollowSymlinks{return errors.New("follow_symlinks is not supported in this version")}
 for i,p:=range f.AllowedRoots{v,e:=expandPath(p);if e!=nil{return e};f.AllowedRoots[i]=v}
 if f.DefaultWorkdir!=""{v,e:=expandPath(f.DefaultWorkdir);if e!=nil{return e};f.DefaultWorkdir=v}
 if c.Batch.MaxOperations<1||c.Batch.MaxOperations>64||c.Batch.MaxParallel<1||c.Batch.MaxParallel>16||c.Batch.MaxOutputBytes<1024||c.Batch.MaxOutputBytes>4194304{return errors.New("invalid batch limits")}
 for _,n:=range c.Tools.Enabled{if n!="shell"&&len(f.AllowedRoots)==0{return errors.New("native tools require explicit filesystem.allowed_roots")}}
 return nil
}
type fileLocation struct{root *os.Root; name string; absolute string}
func(l fileLocation) close(){_ = l.root.Close()}
func (f FilesystemConfig) locate(p string)(fileLocation,error){
 if p==""||len(p)>4096||strings.ContainsRune(p,0){return fileLocation{},errors.New("invalid path")}
 if !filepath.IsAbs(p){if f.DefaultWorkdir==""{return fileLocation{},errors.New("absolute path required")};p=filepath.Join(f.DefaultWorkdir,p)}
 p=filepath.Clean(p)
 for _,base:=range f.AllowedRoots{
  rel,e:=filepath.Rel(base,p)
  if e!=nil||rel==".."||strings.HasPrefix(rel,".."+string(os.PathSeparator)){continue}
  root,e:=os.OpenRoot(base);if e!=nil{return fileLocation{},e}
  l:=fileLocation{root,rel,p}
  // Root confines resolution even if paths change concurrently. Reject observed symlinks.
  parts:=strings.Split(rel,string(os.PathSeparator));cur:="."
  for _,part:=range parts{cur=filepath.Join(cur,part);st,e:=root.Lstat(cur);if errors.Is(e,fs.ErrNotExist){break};if e!=nil{l.close();return fileLocation{},e};if st.Mode()&os.ModeSymlink!=0{l.close();return fileLocation{},errors.New("symlink traversal disabled")}}
  return l,nil
 }
 return fileLocation{},errors.New("path outside allowed roots")
}
func hashBytes(b []byte)string{h:=sha256.Sum256(b);return hex.EncodeToString(h[:])}
func readText(l fileLocation,limit int)([]byte,fs.FileInfo,error){
 st,e:=l.root.Lstat(l.name);if e!=nil{return nil,nil,e}
 if !st.Mode().IsRegular(){return nil,nil,errors.New("regular text file required")}
 f,e:=l.root.OpenFile(l.name,os.O_RDONLY|nonblockFlag(),0);if e!=nil{return nil,nil,e};defer f.Close()
 st,e=f.Stat();if e!=nil{return nil,nil,e};if !st.Mode().IsRegular(){return nil,nil,errors.New("regular file required")}
 b,e:=io.ReadAll(io.LimitReader(f,int64(limit)+1));if e!=nil{return nil,nil,e}
 if len(b)>limit{return nil,nil,errors.New("file exceeds read limit")}
 if !utf8.Valid(b)||bytes.IndexByte(b,0)>=0{return nil,nil,errors.New("binary or non-UTF-8 file")}
 return b,st,nil
}
type ReadInput struct{Path string `json:"path"`;StartLine int `json:"start_line"`;EndLine int `json:"end_line"`}
type ReadOutput struct{Path string `json:"path"`;StartLine int `json:"start_line"`;EndLine int `json:"end_line"`;TotalLines int `json:"total_lines"`;Content string `json:"content"`;SHA256 string `json:"sha256"`;Truncated bool `json:"truncated"`}
func linesOf(s string)[]string{if s==""{return nil};a:=strings.SplitAfter(s,"\n");if a[len(a)-1]==""{a=a[:len(a)-1]};return a}
func fileRead(ctx context.Context,c Config,in ReadInput)(ReadOutput,error){
 var out ReadOutput
 if e:=ctx.Err();e!=nil{return out,e}
 if in.StartLine<1||in.EndLine<in.StartLine{return out,errors.New("invalid inclusive line range")}
 l,e:=c.Filesystem.locate(in.Path);if e!=nil{return out,e};defer l.close()
 b,_,e:=readText(l,c.Filesystem.MaxReadBytes);if e!=nil{return out,e}
 lines:=linesOf(string(b));end:=min(in.EndLine,len(lines));start:=min(in.StartLine-1,len(lines))
 out=ReadOutput{Path:l.absolute,StartLine:in.StartLine,EndLine:end,TotalLines:len(lines),Content:strings.Join(lines[start:end],""),SHA256:hashBytes(b)}
 return out,nil
}
type InfoInput struct{Paths []string `json:"paths"`;Hash bool `json:"hash,omitempty"`}
type InfoEntry struct{Path string `json:"path"`;Exists bool `json:"exists"`;Type string `json:"type,omitempty"`;Size int64 `json:"size"`;Mode string `json:"mode,omitempty"`;Modified string `json:"modified,omitempty"`;SHA256 string `json:"sha256,omitempty"`;Error string `json:"error,omitempty"`}
type InfoOutput struct{Entries []InfoEntry `json:"entries"`}
func fileInfo(ctx context.Context,c Config,in InfoInput)(InfoOutput,error){
 out:=InfoOutput{Entries:[]InfoEntry{}}
 if len(in.Paths)<1||len(in.Paths)>c.Filesystem.MaxSearchResults{return out,errors.New("invalid number of paths")}
 for _,p:=range in.Paths{if e:=ctx.Err();e!=nil{return out,e};v:=InfoEntry{Path:p};func(){
  l,e:=c.Filesystem.locate(p);if e!=nil{v.Error=e.Error();return};defer l.close();v.Path=l.absolute
  st,e:=l.root.Lstat(l.name);if errors.Is(e,fs.ErrNotExist){return};if e!=nil{v.Error=e.Error();return}
  v.Exists=true;v.Size=st.Size();v.Mode=st.Mode().String();v.Modified=st.ModTime().UTC().Format("2006-01-02T15:04:05Z");v.Type=kind(st)
  if in.Hash&&st.Mode().IsRegular(){b,_,e:=readText(l,c.Filesystem.MaxReadBytes);if e!=nil{v.Error=e.Error()}else{v.SHA256=hashBytes(b)}}
 }();out.Entries=append(out.Entries,v)}
 return out,nil
}
func kind(st fs.FileInfo)string{if st.IsDir(){return "directory"};if st.Mode().IsRegular(){return "file"};if st.Mode()&os.ModeSymlink!=0{return "symlink"};return "other"}
type WriteInput struct{Path string `json:"path"`;Content string `json:"content"`;Overwrite bool `json:"overwrite,omitempty"`;CreateParents bool `json:"create_parents,omitempty"`;ExpectedSHA256 string `json:"expected_sha256,omitempty"`}
type EditInput struct{Path string `json:"path"`;OldString string `json:"old_string,omitempty"`;NewString string `json:"new_string,omitempty"`;ReplaceAll bool `json:"replace_all,omitempty"`;ExpectedSHA256 string `json:"expected_sha256,omitempty"`;StartLine int `json:"start_line,omitempty"`;EndLine int `json:"end_line,omitempty"`;Content string `json:"content,omitempty"`}
type ChangeOutput struct{Path string `json:"path"`;Edited bool `json:"edited"`;Replacements int `json:"replacements"`;SHA256 string `json:"sha256"`;Bytes int `json:"bytes"`}
var mutationMu sync.Mutex
func replaceFile(l fileLocation,b []byte,mode fs.FileMode,overwrite bool)error{
 // Temporary file in the destination directory; root-relative operations retain confinement.
 parent:=filepath.Dir(l.name)
 var tmp string;var f *os.File;var e error
 for i:=0;i<10;i++{tmp=filepath.Join(parent,".machine-"+randomSuffix());f,e=l.root.OpenFile(tmp,os.O_WRONLY|os.O_CREATE|os.O_EXCL,0600);if !errors.Is(e,fs.ErrExist){break}}
 if e!=nil{return e};defer l.root.Remove(tmp)
 if _,e=f.Write(b);e==nil{e=f.Chmod(mode.Perm())};if e==nil{e=f.Sync()};ce:=f.Close();if e==nil{e=ce};if e!=nil{return e}
 if !overwrite{return l.root.Link(tmp,l.name)}
 return l.root.Rename(tmp,l.name)
}
func fileWrite(ctx context.Context,c Config,in WriteInput)(ChangeOutput,error){
 var out ChangeOutput;mutationMu.Lock();defer mutationMu.Unlock()
 if e:=ctx.Err();e!=nil{return out,e}
 if len(in.Content)>c.Filesystem.MaxWriteBytes||!utf8.ValidString(in.Content)||strings.ContainsRune(in.Content,0){return out,errors.New("invalid or oversized text")}
 l,e:=c.Filesystem.locate(in.Path);if e!=nil{return out,e};defer l.close()
 st,e:=l.root.Lstat(l.name);exists:=e==nil;mode:=fs.FileMode(0600)
 if exists{if !in.Overwrite||!st.Mode().IsRegular(){return out,errors.New("destination exists or is not a regular file")};mode=st.Mode().Perm()}else if !errors.Is(e,fs.ErrNotExist){return out,e}
 if in.ExpectedSHA256!=""{b,_,e:=readText(l,c.Filesystem.MaxReadBytes);if e!=nil{return out,e};if hashBytes(b)!=in.ExpectedSHA256{return out,errors.New("file changed since read")}}
 if in.CreateParents{if e=l.root.MkdirAll(filepath.Dir(l.name),0700);e!=nil{return out,e}}
 b:=[]byte(in.Content);if e=ctx.Err();e!=nil{return out,e};if e=replaceFile(l,b,mode,exists);e!=nil{return out,e}
 return ChangeOutput{l.absolute,true,1,hashBytes(b),len(b)},nil
}
func fileEdit(ctx context.Context,c Config,in EditInput)(ChangeOutput,error){
 var out ChangeOutput;mutationMu.Lock();defer mutationMu.Unlock()
 if e:=ctx.Err();e!=nil{return out,e}
 l,e:=c.Filesystem.locate(in.Path);if e!=nil{return out,e};defer l.close()
 b,st,e:=readText(l,c.Filesystem.MaxReadBytes);if e!=nil{return out,e}
 if in.ExpectedSHA256!=""&&hashBytes(b)!=in.ExpectedSHA256{return out,errors.New("file changed since read")}
 s:=string(b);n:=1
 if in.OldString!=""{
  if in.StartLine!=0||in.EndLine!=0{return out,errors.New("choose pattern or line range")}
  n=strings.Count(s,in.OldString);if n==0||n>1&&!in.ReplaceAll{return out,fmt.Errorf("expected unique match, found %d",n)}
  delta:=len(in.NewString)-len(in.OldString);if delta>0&&(len(s)>c.Filesystem.MaxWriteBytes||n>(c.Filesystem.MaxWriteBytes-len(s))/delta){return out,errors.New("edit exceeds write limit")}
  s=strings.ReplaceAll(s,in.OldString,in.NewString)
 }else{a:=linesOf(s);if in.StartLine<1||in.EndLine<in.StartLine||in.EndLine>len(a){return out,errors.New("invalid edit range")};s=strings.Join(a[:in.StartLine-1],"")+in.Content+strings.Join(a[in.EndLine:],"")}
 if len(s)>c.Filesystem.MaxWriteBytes||!utf8.ValidString(s)||strings.ContainsRune(s,0){return out,errors.New("invalid or oversized edit")}
 if e=ctx.Err();e!=nil{return out,e}
 if e=replaceFile(l,[]byte(s),st.Mode(),true);e!=nil{return out,e}
 return ChangeOutput{l.absolute,true,n,hashBytes([]byte(s)),len(s)},nil
}
