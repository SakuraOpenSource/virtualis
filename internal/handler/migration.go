package handler

import (
 "github.com/SakuraOpenSource/virtualis/internal/service"
 "github.com/gin-gonic/gin"
)
func(h *Handler)ResizeInstance(c *gin.Context){id,ok:=IDParam(c,"id");if !ok{return};var req service.ResizeInput;if !bindJSON(c,&req){return};item,err:=h.virtualis().ResizeInstance(c.Request.Context(),id,req);respond(c,item,err)}
func(h *Handler)MigrateInstance(c *gin.Context){id,ok:=IDParam(c,"id");if !ok{return};var req service.MigrationInput;if !bindJSON(c,&req){return};item,err:=h.virtualis().MigrateInstance(c.Request.Context(),id,req);respond(c,item,err)}
func(h *Handler)Migrations(c *gin.Context){id,ok:=IDParam(c,"id");if !ok{return};items,err:=h.virtualis().ListMigrations(id);if err!=nil{respond(c,nil,err);return};OK(c,gin.H{"items":items})}
