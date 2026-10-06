package handler

import (
 "github.com/SakuraOpenSource/virtualis/internal/httpx"
 "github.com/SakuraOpenSource/virtualis/internal/service"
 "github.com/gin-gonic/gin"
)

func(h *Handler)Trash(c *gin.Context){
 page,size,_:=Pagination(c);ownerID:=uint(0);if user:=httpx.CurrentUser(c);user!=nil&&!user.IsAdmin(){ownerID=user.ID}
 items,total,err:=h.virtualis().ListTrash(page,size,ownerID);if err!=nil{respond(c,nil,err);return};OK(c,Page{Items:items,Total:total,Page:page,PageSize:size})
}
func(h *Handler)RestoreTrash(c *gin.Context){id,ok:=IDParam(c,"id");if !ok{return};inst,err:=h.virtualis().RestoreTrashedInstance(c.Request.Context(),id);respond(c,inst,err)}
func(h *Handler)PurgeInstance(c *gin.Context){id,ok:=IDParam(c,"id");if !ok{return};if err:=h.virtualis().PurgeInstance(c.Request.Context(),id);err!=nil{respond(c,nil,err);return};noContent(c)}
func(h *Handler)BatchInstances(c *gin.Context){
 var req service.BatchInput;if !bindJSON(c,&req){return};ownerID:=uint(0);if user:=httpx.CurrentUser(c);user!=nil&&!user.IsAdmin(){ownerID=user.ID}
 result,err:=h.virtualis().BatchInstances(c.Request.Context(),req,ownerID);respond(c,result,err)
}
func(h *Handler)Retention(c *gin.Context){item,err:=h.virtualis().Retention();respond(c,item,err)}
func(h *Handler)SaveRetention(c *gin.Context){var req service.RetentionSettings;if !bindJSON(c,&req){return};item,err:=h.virtualis().SaveRetention(req);respond(c,item,err)}
