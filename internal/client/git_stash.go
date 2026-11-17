package client

import (
 "context"
 "encoding/json"
 "slices"
 "strings"

 "posse/internal/gitx"
 "posse/internal/rpc"
)

type GitStashOperation struct {
 operation *DesktopOperation
 expected rpc.GitStashExpectation
}
type GitStashStatus struct {
 Operation rpc.DesktopOperation
 Result *rpc.GitStashResult
}
func (c *Client) GitStashState(ctx context.Context,target rpc.GitWriteTarget)(rpc.GitStashStateResult,error){
 var out rpc.GitStashStateResult
 if !rpc.ValidGitFetchParams(target){return out,rpc.Errorf(rpc.CodeBadRequest,"invalid Git stash parameters")}
 if err:=c.Call(ctx,"git.stashState",target,&out);err!=nil{return out,err}
 if out.Target!=target || !gitx.ValidStashBaseline(out.Baseline){return rpc.GitStashStateResult{},rpc.Errorf(rpc.CodeInternal,"invalid Git stash result")}
 return out,nil
}
func(c *Client)PrepareGitStashPush(p rpc.GitStashPushParams)(*GitStashOperation,error){return c.prepareGitStash("push",p.Target,p.Baseline,"",p.Message,p)}
func(c *Client)PrepareGitStashApply(p rpc.GitStashApplyParams)(*GitStashOperation,error){return c.prepareGitStash("apply",p.Target,p.Baseline,p.Hash,"",p)}
func(c *Client)PrepareGitStashPop(p rpc.GitStashPopParams)(*GitStashOperation,error){return c.prepareGitStash("pop",p.Target,p.Baseline,p.Hash,"",p)}
func(c *Client)PrepareGitStashDrop(p rpc.GitStashDropParams)(*GitStashOperation,error){return c.prepareGitStash("drop",p.Target,gitx.StashBaseline{},p.Hash,"",p)}
func(c *Client)prepareGitStash(action string,target rpc.GitWriteTarget,baseline gitx.StashBaseline,hash,message string,params any)(*GitStashOperation,error){
 if !rpc.ValidGitStashParams(action,target,baseline,hash,message){return nil,rpc.Errorf(rpc.CodeBadRequest,"invalid Git stash parameters")}
 op,err:=c.PrepareDesktopOperation(stashMethod(action),params);if err!=nil{return nil,err}
 return &GitStashOperation{operation:op,expected:rpc.GitStashExpectation{Target:target,Action:action,Hash:hash}},nil
}
func stashMethod(action string)string{return "git.stash"+strings.ToUpper(action[:1])+action[1:]}
func(op *GitStashOperation)Ref()rpc.DesktopOperationRef{if op==nil{return rpc.DesktopOperationRef{}};return op.operation.Ref()}
func(op *GitStashOperation)Expectation()rpc.GitStashExpectation{if op==nil{return rpc.GitStashExpectation{}};return op.expected}
func(op *GitStashOperation)Execute(ctx context.Context)(rpc.GitStashResult,error){
 var out rpc.GitStashResult
 if op==nil||op.operation==nil{return out,rpc.Errorf(rpc.CodeBadRequest,"invalid desktop operation reference")}
 if err:=op.operation.Execute(ctx,&out);err!=nil{return rpc.GitStashResult{},err}
 if !validGitStashResult(out,op.expected){err:=rpc.Errorf(rpc.CodeOutcomeUnknown,"invalid Git stash result");err.Data,_=json.Marshal(op.Ref());return rpc.GitStashResult{},err}
 return out,nil
}
func(c *Client)GitStashStatus(ctx context.Context,ref rpc.DesktopOperationRef,expected rpc.GitStashExpectation)(GitStashStatus,error){
 if !rpc.ValidGitStashExpectation(expected){return GitStashStatus{},rpc.Errorf(rpc.CodeBadRequest,"invalid Git stash parameters")}
 op,err:=c.DesktopOperationStatus(ctx,ref);if err!=nil{return GitStashStatus{},err}
 out:=GitStashStatus{Operation:op}
 if op.State=="unknown"{return out,nil}
 if op.Method!=stashMethod(expected.Action){return GitStashStatus{},rpc.Errorf(rpc.CodeInternal,"invalid Git stash result")}
 if op.State!="done"||op.Error!=nil{return out,nil}
 var result rpc.GitStashResult
 if len(op.Result)>rpc.GitStashResultLimit||json.Unmarshal(op.Result,&result)!=nil||!validGitStashResult(result,expected){return GitStashStatus{},rpc.Errorf(rpc.CodeInternal,"invalid Git stash result")}
 out.Result=&result;return out,nil
}
func validGitStashResult(out rpc.GitStashResult,expected rpc.GitStashExpectation)bool{
 if out.Target!=expected.Target||out.Action!=expected.Action||out.Hash!=expected.Hash{return false}
 switch out.DropState {
 case rpc.GitStashDropNotAttempted:
 case rpc.GitStashDropSucceeded,rpc.GitStashDropFailed,rpc.GitStashDropUnknown:
  if !out.Started || out.Action!="drop" && (out.Action!="pop"||!out.Applied){return false}
 default:return false
 }
 if out.Applied && (!out.Started || out.Action!="apply"&&out.Action!="pop"){return false}
 if out.Status==rpc.GitFetchSucceeded {
  if (out.Action=="apply"||out.Action=="pop")&&!out.Applied{return false}
  if (out.Action=="drop"||out.Action=="pop")&&out.DropState!=rpc.GitStashDropSucceeded{return false}
 } else if out.DropState==rpc.GitStashDropSucceeded{return false}
 validArgs:=func(args []string)bool{
  if len(args)<2||args[0]!="stash"{return false}
  switch args[1] {
  case "push":return out.Action=="push" && (slices.Equal(args,[]string{"stash","push","--include-untracked"}) || len(args)==5&&args[2]=="--include-untracked"&&args[3]=="-m"&&args[4]=="[message omitted]")
  case "apply":return (out.Action=="apply"||out.Action=="pop")&&slices.Equal(args,[]string{"stash","apply","--index",out.Hash})
  case "drop":return (out.Action=="drop"||out.Action=="pop"&&out.Applied)&&len(args)==3&&validStashLogRef(args[2])
  };return false
 }
 if !validGitWriteResult(out.GitFetchResult,expected.Target,"stash",validArgs){return false}
 raw,err:=json.Marshal(out);return err==nil&&len(raw)<=rpc.GitStashResultLimit
}
func validStashLogRef(ref string)bool{
 if !strings.HasPrefix(ref,"stash@{")||!strings.HasSuffix(ref,"}"){return false}
 n:=ref[7:len(ref)-1];return n!=""&&strings.Trim(n,"0123456789")==""
}
