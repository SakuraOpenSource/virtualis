package service

import "context"

type BatchInput struct{IDs []uint `json:"ids"`;Action string `json:"action"`}

// Serial execution is intentional: it bounds hypervisor work while preserving
// a distinct outcome for each unique input, including permission failures.
func(s *VirtualisService)BatchInstances(ctx context.Context,req BatchInput,ownerID uint)(BatchResult,error){
 out:=BatchResult{OK:[]uint{},Failed:[]BatchFailure{}}
 if len(req.IDs)==0||len(req.IDs)>100{return out,BadRequest("batch must contain 1-100 entries")}
 switch req.Action{case "start","stop","restart","delete":default:return out,BadRequest("unsupported batch action")}
 seen:=map[uint]bool{}
 for _,id:=range req.IDs{
  if seen[id]{continue};seen[id]=true
  inst,err:=s.GetInstance(id)
  if err==nil&&ownerID!=0&&(inst.OwnerID==nil||*inst.OwnerID!=ownerID){err=Forbidden("instance ownership required")}
  if err==nil{err=ctx.Err()}
  if err==nil{if req.Action=="delete"{err=s.DeleteInstance(ctx,id)}else{_,err=s.PowerInstance(ctx,id,req.Action,nil)}}
  if err!=nil{out.Failed=append(out.Failed,BatchFailure{ID:id,Reason:err.Error()})}else{out.OK=append(out.OK,id)}
 }
 return out,nil
}
