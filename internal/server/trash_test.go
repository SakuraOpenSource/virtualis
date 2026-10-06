package server

import (
 "fmt"
 "testing"
)

func TestTrashAndBatchSessionAndV1Routes(t *testing.T){
 f:=newAPIFixture(t)
 for _,prefix:=range []string{"/api","/api/v1"}{
  key:="";user:=&f.admin;if prefix=="/api/v1"{key=f.key;user=nil}
  res:=f.request(t,"DELETE",fmt.Sprintf("%s/instances/%d",prefix,f.inst.ID),"",key,user);if res.Code!=204{t.Fatalf("recycle %s: %d %s",prefix,res.Code,res.Body.String())}
  res=f.request(t,"GET",prefix+"/trash","",key,user);if res.Code!=200{t.Fatalf("list trash %s: %d",prefix,res.Code)}
  res=f.request(t,"POST",fmt.Sprintf("%s/trash/%d/restore",prefix,f.inst.ID),"",key,user);if res.Code!=200{t.Fatalf("restore %s: %d %s",prefix,res.Code,res.Body.String())}
  res=f.request(t,"POST",prefix+"/instances/batch",`{"ids":[999],"action":"delete"}`,key,user);if res.Code!=200{t.Fatalf("batch %s: %d %s",prefix,res.Code,res.Body.String())}
 }
 res:=f.request(t,"DELETE",fmt.Sprintf("/api/v1/instances/%d/purge",f.inst.ID),"",f.key,nil);if res.Code!=204{t.Fatalf("purge: %d %s",res.Code,res.Body.String())}
}
