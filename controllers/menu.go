package controllers

import (
	"github.com/gin-gonic/gin"

	"server/cache"
	"server/database"
	"server/models"
	"server/utils"
)

// MenuReq 菜单新增/编辑
type MenuReq struct {
	ID        uint   `json:"id"`
	ParentID  uint   `json:"parentId"`
	Name      string `json:"name"`
	Title     string `json:"title" binding:"required"`
	Path      string `json:"path"`
	Component string `json:"component"`
	Icon      string `json:"icon"`
	Perms     string `json:"perms"`
	Type      string `json:"type"`
	Sort      int    `json:"sort"`
	Visible   int    `json:"visible"`
	Status    int    `json:"status"`
}

// MenuTree 菜单树（含按钮，用于菜单管理页面展示全部）
func MenuTree(c *gin.Context) {
	utils.OK(c, database.GetAllMenuTree())
}

// MenuSave 新增/编辑菜单
func MenuSave(c *gin.Context) {
	var req MenuReq
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}
	if req.Type == "" {
		req.Type = "C"
	}
	if req.Status == 0 {
		req.Status = 1
	}
	if req.Visible == 0 {
		req.Visible = 1
	}

	if req.ID == 0 {
		m := models.Menu{
			ParentID: req.ParentID, Name: req.Name, Title: req.Title,
			Path: req.Path, Component: req.Component, Icon: req.Icon,
			Perms: req.Perms, Type: req.Type, Sort: req.Sort,
			Visible: req.Visible, Status: req.Status,
		}
		if err := database.DB.Create(&m).Error; err != nil {
			utils.ServerError(c, "新增失败")
			return
		}
		cache.InvalidateMenus()
		cache.InvalidatePerms()
		utils.OKMsg(c, "新增成功", m)
		return
	}

	var m models.Menu
	if err := database.DB.First(&m, req.ID).Error; err != nil {
		utils.BadRequest(c, "菜单不存在")
		return
	}
	m.ParentID = req.ParentID
	m.Name = req.Name
	m.Title = req.Title
	m.Path = req.Path
	m.Component = req.Component
	m.Icon = req.Icon
	m.Perms = req.Perms
	m.Type = req.Type
	m.Sort = req.Sort
	m.Visible = req.Visible
	if err := database.DB.Save(&m).Error; err != nil {
		utils.ServerError(c, "更新失败")
		return
	}
	cache.InvalidateMenus()
	cache.InvalidatePerms()
	utils.OKMsg(c, "编辑成功", m)
}

// MenuDelete 删除菜单（有子菜单时禁止删除）
func MenuDelete(c *gin.Context) {
	id := c.Param("id")
	var child int64
	database.DB.Model(&models.Menu{}).Where("parent_id = ?", id).Count(&child)
	if child > 0 {
		utils.BadRequest(c, "请先删除子菜单")
		return
	}
	database.DB.Delete(&models.Menu{}, id)
	database.DB.Where("menu_id = ?", id).Delete(&models.RoleMenu{})
	cache.InvalidateMenus()
	cache.InvalidatePerms()
	utils.OKMsg(c, "删除成功", nil)
}

// MenuSort 批量更新排序（前端上移/下移后保存整组顺序）
func MenuSort(c *gin.Context) {
	var list []struct {
		ID   uint `json:"id"`
		Sort int  `json:"sort"`
	}
	if err := c.ShouldBindJSON(&list); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}
	for _, it := range list {
		if it.ID == 0 {
			continue
		}
		if err := database.DB.Model(&models.Menu{}).
			Where("id = ?", it.ID).Update("sort", it.Sort).Error; err != nil {
			utils.ServerError(c, "保存排序失败")
			return
		}
	}
	cache.InvalidateMenus()
	utils.OKMsg(c, "排序已更新", nil)
}

// MenuStatus 启用/停用菜单
func MenuStatus(c *gin.Context) {
	var req struct {
		ID     uint `json:"id"`
		Status int  `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}
	if req.ID == 0 {
		utils.BadRequest(c, "参数错误")
		return
	}
	if req.Status != 0 && req.Status != 1 {
		utils.BadRequest(c, "状态值不合法")
		return
	}
	if err := database.DB.Model(&models.Menu{}).
		Where("id = ?", req.ID).Update("status", req.Status).Error; err != nil {
		utils.ServerError(c, "更新失败")
		return
	}
	cache.InvalidateMenus()
	utils.OKMsg(c, "更新成功", nil)
}