package gitx

import (
 "context"
 "crypto/sha256"
 "encoding/hex"
 "encoding/json"
 "errors"
 "os"
 "os/exec"
 "slices"
 "strings"
 "time"
)

var (
 ErrStashChanged = errors.New("Git stash preparation changed; refresh before trying again")
 ErrStashLimit = errors.New("Git stash preparation exceeds the scan limit")
 ErrStashBusy = errors.New("Git stash operation requires a resolved index and no operation in progress")
 ErrStashMissing = errors.New("this stash no longer exists")
 ErrStashDuplicate = errors.New("multiple stash entries have this hash; refresh and resolve the duplicate entries")
)

type StashBaseline struct {
 HeadOID string `json:"headOID"`
 HeadRef string `json:"headRef"`
 IndexDigest string `json:"indexDigest"`
 WorktreeDigest string `json:"worktreeDigest"`
}
func ValidStashHash(s string) bool { return ValidBlobOID(s) && strings.Trim(s,"0")!="" }
func ValidStashBaseline(b StashBaseline) bool {
 validDigest:=func(s string)bool { raw,err:=hex.DecodeString(s); return err==nil && len(raw)==32 && strings.ToLower(s)==s }
 return (b.HeadOID=="" || ValidStashHash(b.HeadOID)) && (b.HeadRef=="" || strings.HasPrefix(b.HeadRef,"refs/heads/") && ValidReadRevision(b.HeadRef)) && (b.HeadOID!="" || b.HeadRef!="") && validDigest(b.IndexDigest) && validDigest(b.WorktreeDigest)
}
func stashDigest(b []byte) string {h:=sha256.Sum256(b);return hex.EncodeToString(h[:])}

// ReadStashBaseline 只摘要变化路径；两轮检查共享预算，不将准备结果宣称为外部编辑的原子快照。
func ReadStashBaseline(ctx context.Context, id RepositoryIdentity) (StashBaseline,error) {
 ctx,cancel:=context.WithTimeout(ctx,30*time.Second);defer cancel()
 ctx=WithReadLimit(WithTrace(ctx,nil),4<<20)
 budget:=&worktreeBudget{bytes:WorktreeScanLimit}
 before,err:=stashBaseline(ctx,id,budget)
 if err!=nil{return StashBaseline{},stashStateError(err)}
 after,err:=stashBaseline(ctx,id,budget)
 if err!=nil{return StashBaseline{},stashStateError(err)}
 if before!=after{return StashBaseline{},ErrStashChanged}
 if err:=CheckRepositoryIdentity(ctx,id.Root,id);err!=nil{return StashBaseline{},err}
 return after,ctx.Err()
}
func stashStateError(err error) error {
 if errors.Is(err,ErrWorktreeLimit)||errors.Is(err,ErrReadLimit){return ErrStashLimit}
 if errors.Is(err,ErrWorktreeChanged){return ErrStashChanged}
 return err
}
func stashBaseline(ctx context.Context,id RepositoryIdentity,budget *worktreeBudget)(StashBaseline,error){
 var out StashBaseline
 if err:=CheckRepositoryIdentity(ctx,id.Root,id);err!=nil{return out,err}
 st,err:=ReadStatusChecked(ctx,id.Root);if err!=nil{return out,err}
 if st.Operation!="" || slices.ContainsFunc(st.Entries,func(e Entry)bool{return e.Conflict}) {return out,ErrStashBusy}
 if st.Commit!="" && !ValidStashHash(st.Commit){return out,ErrStashChanged}
 out.HeadOID=st.Commit
 ref,err:=runRaw(ctx,id.Root,nil,nil,"symbolic-ref","-q","HEAD")
 if err!=nil {var exit *exec.ExitError;if !errors.As(err,&exit)||exit.ExitCode()!=1{return out,err}} else {out.HeadRef=strings.TrimSuffix(string(ref),"\n")}
 index,err:=runRaw(ctx,id.Root,nil,nil,"ls-files","--stage","-v","-z");if err!=nil{return out,err}
 out.IndexDigest=stashDigest(index)
 if len(st.Entries)>worktreePathLimit {return out,ErrStashLimit}
 paths:=[]string{}
 for _,e:=range st.Entries {
  for _,name:=range []string{e.Path,e.Orig} {if name==""{continue}; name=strings.TrimSuffix(name,"/");if !ValidBlameFile(name){return out,ErrStashChanged};paths=append(paths,name)}
 }
 slices.Sort(paths);paths=slices.Compact(paths)
 if len(paths)>worktreePathLimit{return out,ErrStashLimit}
 root,err:=os.OpenRoot(id.Root);if err!=nil{return out,err};defer root.Close()
 // 不将上游计数放入基线，后台 fetch 不应使工作树准备失效。
 snapshot:=struct{Entries []Entry; Files []WorktreeFile}{Entries:st.Entries}
 for _,name:=range paths {
  if err:=ctx.Err();err!=nil{return out,err}
  file,err:=worktreeVersion(ctx,root,id.Root,name,budget);if err!=nil{return out,err}
  snapshot.Files=append(snapshot.Files,file)
 }
 raw,err:=json.Marshal(snapshot);if err!=nil{return out,err}
 if len(raw)>4<<20{return out,ErrStashLimit}
 out.WorktreeDigest=stashDigest(raw)
 if !ValidStashBaseline(out){return out,ErrStashChanged}
 return out,nil
}

func StashRefForHash(ctx context.Context,dir,hash string)(string,error){
 if !ValidStashHash(hash){return "",ErrStashMissing}
 ctx=WithReadLimit(WithTrace(ctx,nil),4<<20)
 list,err:=Stashes(ctx,dir);if err!=nil{return "",err}
 ref:=""
 for _,s:=range list {if s.Hash==hash {if ref!=""{return "",ErrStashDuplicate};ref=s.Ref}}
 if ref==""{return "",ErrStashMissing}
 return ref,nil
}
