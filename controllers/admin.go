package controllers

import (
	"github.com/gin-gonic/gin"

	"server/cache"
	"server/database"
	"server/models"
	"server/utils"
)

// AdminReq 管理员新增/编辑
type AdminReq struct {
	ID       uint   `json:"id"`
	Username string `json:"username" binding:"required"`
	Password string `json:"password"`
	Nickname string `json:"nickname"`
	Avatar   string `json:"avatar"`
	Email    string `json:"email"`
	Mobile   string `json:"mobile"`
	RoleID   uint   `json:"roleId"`
	Status   int    `json:"status"`
}

// AdminList 分页查询管理员
func AdminList(c *gin.Context) {
	page, size := utils.Page(c)
	keyword := c.Query("keyword")
	roleID := c.Query("roleId")
	status := c.Query("status")

	q := database.DB.Model(&models.Admin{}).Preload("Role")
	if keyword != "" {
		like := "%" + keyword + "%"
		q = q.Where("username LIKE ? OR nickname LIKE ? OR email LIKE ?", like, like, like)
	}
	if roleID != "" {
		q = q.Where("role_id = ?", roleID)
	}
	if status != "" {
		q = q.Where("status = ?", status)
	}

	var total int64
	q.Count(&total)

	var list []models.Admin
	q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list)

	utils.OK(c, utils.PageData{List: list, Total: total, Page: page, Size: size})
}

// AdminSave 新增/编辑管理员
func AdminSave(c *gin.Context) {
	var req AdminReq
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}

	if req.ID == 0 {
		// 新增
		var count int64
		database.DB.Model(&models.Admin{}).Where("username = ?", req.Username).Count(&count)
		if count > 0 {
			utils.BadRequest(c, "账号已存在")
			return
		}
		if req.Password == "" {
			utils.BadRequest(c, "请设置密码")
			return
		}
		hash, err := database.Hash(req.Password)
		if err != nil {
			utils.ServerError(c, "加密失败")
			return
		}
		admin := models.Admin{
			Username: req.Username, Password: hash, Nickname: req.Nickname,
			Avatar: req.Avatar, Email: req.Email, Mobile: req.Mobile,
			RoleID: req.RoleID, Status: req.Status,
		}
		if req.Status == 0 {
			admin.Status = 1
		}
		if err := database.DB.Create(&admin).Error; err != nil {
			utils.ServerError(c, "新增失败")
			return
		}
		cache.InvalidateAdmin(admin.ID)
		utils.OKMsg(c, "新增成功", admin)
		return
	}

	// 编辑
	var admin models.Admin
	if err := database.DB.First(&admin, req.ID).Error; err != nil {
		utils.BadRequest(c, "管理员不存在")
		return
	}
	// 禁止禁用/修改超管 admin 自身
	admin.Nickname = req.Nickname
	admin.Email = req.Email
	admin.Mobile = req.Mobile
	admin.Avatar = req.Avatar
	admin.RoleID = req.RoleID
	if req.Password != "" {
		hash, err := database.Hash(req.Password)
		if err != nil {
			utils.ServerError(c, "加密失败")
			return
		}
		admin.Password = hash
	}
	if err := database.DB.Save(&admin).Error; err != nil {
		utils.ServerError(c, "更新失败")
		return
	}
	// 管理员资料/角色/密码变更：失效鉴权缓存（禁用即时生效）
	cache.InvalidateAdmin(admin.ID)
	utils.OKMsg(c, "编辑成功", admin)
}

// AdminStatus 启用/禁用
func AdminStatus(c *gin.Context) {
	var req struct {
		ID     uint `json:"id" binding:"required"`
		Status int  `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}
	if req.ID == 1 {
		utils.BadRequest(c, "超级管理员不可禁用")
		return
	}
	if err := database.DB.Model(&models.Admin{}).
		Where("id = ?", req.ID).Update("status", req.Status).Error; err != nil {
		utils.ServerError(c, "操作失败")
		return
	}
	// 启用/禁用：鉴权缓存即时失效（禁用语义安全红线）
	cache.InvalidateAdmin(req.ID)
	utils.OKMsg(c, "操作成功", nil)
}

// AdminDelete 删除管理员
func AdminDelete(c *gin.Context) {
	id := c.Param("id")
	if id == "1" {
		utils.BadRequest(c, "超级管理员不可删除")
		return
	}
	if err := database.DB.Delete(&models.Admin{}, id).Error; err != nil {
		utils.ServerError(c, "删除失败")
		return
	}
	cache.InvalidateAdmin(parseID(c, "id"))
	utils.OKMsg(c, "删除成功", nil)
}

// AdminAll 全部管理员（简易下拉，无需分页）
func AdminAll(c *gin.Context) {
	var list []models.Admin
	database.DB.Select("id", "username", "nickname").Find(&list)
	utils.OK(c, list)
}